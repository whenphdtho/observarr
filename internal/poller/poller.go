// Package poller runs one supervised probe loop per service. Each shape kind an adapter
// declares (Info().Kinds) is probed on its own schedule: health always at its configured
// interval, other kinds backed off to an idle interval while nobody is watching the live
// state. A failing kind's interval doubles up to a 5-minute ceiling and resets on success. A
// panicking adapter only affects its own probe.
package poller

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"runtime/debug"
	"sync"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
	"observarr/internal/state"
)

// Defaults used for any Config field left at its zero value.
const (
	defaultHealthInterval = 30 * time.Second
	defaultOtherInterval  = 10 * time.Second
	defaultIdle           = 2 * time.Minute
	defaultTimeout        = 10 * time.Second
)

// maxBackoff is the ceiling a kind's interval doubles up to after consecutive failures.
const maxBackoff = 5 * time.Minute

// jitterFraction is the maximum fraction (±) by which a scheduled interval is randomly
// adjusted, so many services polled on the same interval don't all probe in lockstep.
const jitterFraction = 0.10

// waitTick bounds how long a single wait sleeps before re-deriving the target interval. A kind
// idling because Hub.Watchers() == 0 is re-evaluated at least this often, so once a viewer
// subscribes an idle kind notices and speeds up within about this long — the simplest way to
// make polling demand-driven without adding a notification channel to state.Hub.
const waitTick = time.Second

// Service is what the poller needs to probe one app, with its API key already decrypted.
// Defined here, rather than reusing a store type, so this package has no dependency on
// package store.
type Service struct {
	ID      int64
	Type    string
	BaseURL *url.URL
	APIKey  string
}

// Config configures a Poller. Fields left at their zero value take the defaults below.
type Config struct {
	Registry *adapter.Registry
	Hub      *state.Hub
	HTTP     *http.Client
	Log      *slog.Logger

	// Intervals gives each kind's active-mode poll interval. A kind not listed defaults to
	// 30 s for shape.KindHealth, 10 s otherwise.
	Intervals map[shape.Kind]time.Duration
	// Idle is the interval used for non-health kinds while Hub.Watchers() == 0. Default 2 m.
	// Health is always polled at its own interval, watched or not.
	Idle time.Duration
	// Timeout bounds every individual probe. Default 10 s.
	Timeout time.Duration
	// Now returns the current time, used to stamp each shape.Reading. Default time.Now;
	// overridable so tests can assert on exact timestamps. It does not affect scheduling —
	// wait intervals are measured against the real clock regardless.
	Now func() time.Time
}

// Poller keeps one supervised probe loop per service, each running until Stop or Close.
type Poller struct {
	cfg Config

	mu      sync.Mutex
	loops   map[int64]*serviceLoop
	idLocks map[int64]*sync.Mutex // serializes Start/Stop sequences per service id
	closed  bool

	// wg counts every loop goroutine ever started (Add in Start) until it actually exits
	// (Done in run). Close waits on wg instead of on the current contents of loops, so it
	// can't return early for a loop a concurrent Stop has already removed from loops but not
	// yet finished stopping.
	wg sync.WaitGroup
}

// serviceLoop is the bookkeeping for one running service loop.
type serviceLoop struct {
	cancel context.CancelFunc
	done   chan struct{} // closed once the loop (and every per-kind goroutine) has exited
}

// New returns a Poller configured by c. Call Start to begin polling a service.
func New(c Config) *Poller {
	if c.Idle <= 0 {
		c.Idle = defaultIdle
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	intervals := make(map[shape.Kind]time.Duration, len(c.Intervals))
	for k, v := range c.Intervals {
		if v > 0 {
			intervals[k] = v
		}
	}
	c.Intervals = intervals
	return &Poller{cfg: c, loops: make(map[int64]*serviceLoop), idLocks: make(map[int64]*sync.Mutex)}
}

// idLock returns the mutex serializing Start/Stop sequences for id, creating it on first use.
// It is never removed, but is tiny and ids are few and long-lived, so this doesn't matter.
func (p *Poller) idLock(id int64) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.idLocks[id]
	if !ok {
		l = &sync.Mutex{}
		p.idLocks[id] = l
	}
	return l
}

// Start (re)starts the probe loop for svc.ID. A loop already running for that id is stopped
// and waited for first, so at most one loop ever runs per service. The new loop runs until ctx
// is done or Stop/Close is called. A per-id lock (not p.mu) is held for the whole replace
// sequence, so concurrent Start/Stop calls for the *same* id are serialized without blocking
// Start/Stop for any other id.
func (p *Poller) Start(ctx context.Context, svc Service) {
	l := p.idLock(svc.ID)
	l.Lock()
	defer l.Unlock()

	p.stopAndWait(svc.ID)

	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		cancel() // Close already ran; don't leave a loop nothing will ever wait for
		return
	}
	p.wg.Add(1)
	p.loops[svc.ID] = &serviceLoop{cancel: cancel, done: done}
	p.mu.Unlock()

	go p.run(loopCtx, done, svc)
}

// Stop stops the loop for id, if one is running, and waits for it to fully exit.
func (p *Poller) Stop(id int64) {
	l := p.idLock(id)
	l.Lock()
	defer l.Unlock()
	p.stopAndWait(id)
}

// Close cancels every running loop, then waits for all of them to exit. No further Start takes
// effect once Close has run. It waits on the Poller-wide wg (every Start that ever ran, until
// it's really done) rather than on the current contents of loops: a loop a concurrent Stop has
// already pulled out of loops — but not finished stopping — would otherwise not be waited for,
// letting Close return while a probe is still running.
func (p *Poller) Close() {
	p.mu.Lock()
	p.closed = true
	for _, l := range p.loops {
		l.cancel()
	}
	p.loops = make(map[int64]*serviceLoop)
	p.mu.Unlock()

	p.wg.Wait()
}

// stopAndWait cancels and waits for the loop for id, if any. It cancels the loop while still
// holding p.mu (so removal from loops and cancellation happen atomically — Close can never see
// the entry gone but the loop not yet cancelled), then releases the lock before waiting for
// done, so a slow-to-exit loop for one id never blocks Start/Stop for any other id. Callers that
// need this serialized against a concurrent Start/Stop for the *same* id must hold idLock(id)
// first.
func (p *Poller) stopAndWait(id int64) {
	p.mu.Lock()
	l, ok := p.loops[id]
	if ok {
		delete(p.loops, id)
		l.cancel()
	}
	p.mu.Unlock()

	if ok {
		<-l.done
	}
}

// run drives one service's loop: it resolves the adapter, then runs one goroutine per kind the
// adapter declares, each on its own schedule, until ctx is done.
func (p *Poller) run(ctx context.Context, done chan struct{}, svc Service) {
	defer close(done)
	defer p.wg.Done()

	a, ok := p.cfg.Registry.Get(svc.Type)
	if !ok {
		p.cfg.Hub.Record(svc.ID, shape.KindHealth, shape.Reading{Error: "unknown app type", At: p.cfg.Now()})
		return
	}

	kinds := a.Info().Kinds
	var wg sync.WaitGroup
	for _, kind := range kinds {
		wg.Add(1)
		go func(kind shape.Kind) {
			defer wg.Done()
			p.pollKind(ctx, a, svc, kind)
		}(kind)
	}
	wg.Wait()
}

// pollKind repeatedly probes one kind until ctx is done: immediately on the first iteration,
// then after a (possibly backed-off, jittered) wait on every later one.
func (p *Poller) pollKind(ctx context.Context, a adapter.Adapter, svc Service, kind shape.Kind) {
	failures := 0
	first := true
	for {
		if ctx.Err() != nil {
			return
		}
		if !first && !p.wait(ctx, kind, failures) {
			return
		}
		first = false

		if p.probe(ctx, a, svc, kind) {
			failures = 0
		} else {
			failures++
		}
	}
}

// wait blocks until the next probe of kind is due, or ctx is done (returning false). The target
// interval is re-derived from the current Hub.Watchers() count at least every waitTick, so an
// idle kind speeds up soon after a subscriber appears rather than only at the end of a
// possibly long idle sleep. The jitter fraction is fixed for the whole wait so it does not
// wander on every re-check.
func (p *Poller) wait(ctx context.Context, kind shape.Kind, failures int) bool {
	start := time.Now()
	factor := 1 + (rand.Float64()*2-1)*jitterFraction
	var timer *time.Timer // one per wait, not one per tick
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		target := time.Duration(float64(p.backoffInterval(kind, failures)) * factor)
		remaining := target - time.Since(start)
		if remaining <= 0 {
			return true
		}
		d := min(remaining, waitTick)
		if timer == nil {
			timer = time.NewTimer(d)
		} else {
			timer.Reset(d) // its last tick was received, so no stale one can follow
		}
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
		}
	}
}

// backoffInterval is kind's current base interval (active or idle) with zero failures; with
// consecutive failures it doubles, up to a ceiling of max(base, maxBackoff) — never lower than
// the base itself, so back-off never makes a kind poll faster than configured (e.g. an idle
// interval longer than maxBackoff must stay at that length, not shrink to maxBackoff).
func (p *Poller) backoffInterval(kind shape.Kind, failures int) time.Duration {
	base := p.baseInterval(kind)
	ceiling := maxBackoff
	if base > ceiling {
		ceiling = base
	}
	d := base
	for i := 0; i < failures && d < ceiling; i++ {
		d *= 2
	}
	if d > ceiling {
		d = ceiling
	}
	return d
}

// baseInterval is kind's interval with no back-off applied: health always polls at its
// configured interval; other kinds drop to Idle while nobody is watching the live state.
func (p *Poller) baseInterval(kind shape.Kind) time.Duration {
	if kind != shape.KindHealth && p.cfg.Hub.Watchers() == 0 {
		return p.cfg.Idle
	}
	return p.intervalFor(kind)
}

// intervalFor is kind's configured active interval, or the built-in default.
func (p *Poller) intervalFor(kind shape.Kind) time.Duration {
	if d, ok := p.cfg.Intervals[kind]; ok {
		return d
	}
	if kind == shape.KindHealth {
		return defaultHealthInterval
	}
	return defaultOtherInterval
}

// probe runs one deadline-bound, panic-safe probe of kind and records its result. It reports
// whether the probe succeeded, for the caller's back-off bookkeeping.
func (p *Poller) probe(ctx context.Context, a adapter.Adapter, svc Service, kind shape.Kind) bool {
	probeCtx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()

	target := adapter.Target{BaseURL: svc.BaseURL, APIKey: svc.APIKey, HTTP: p.cfg.HTTP}
	data, err := p.safeProbe(probeCtx, a, target, kind)

	if err != nil && ctx.Err() != nil {
		// ctx (the loop's own context, not probeCtx's timeout) is done: this service is being
		// stopped or replaced, and that — not a real probe failure — is why Probe returned an
		// error. Recording it would show a false "down" reading at the exact moment a service
		// is simply restarting. The loop is about to exit on its next iteration regardless, so
		// reporting success here is only to avoid needlessly bumping the failure count.
		return true
	}

	reading := shape.Reading{At: p.cfg.Now()}
	if err != nil {
		reading.Error = readingError(svc.BaseURL.Host, err)
	} else {
		reading.Data = data
	}
	p.cfg.Hub.Record(svc.ID, kind, reading)
	return err == nil
}

var errCrashed = errors.New("adapter crashed")

// readingError is the short text a failed probe's reading shows. It comes from adapter.Describe, so it never
// holds a URL (not even one an app redirected to) or anything else the app sent.
func readingError(host string, err error) string {
	if errors.Is(err, errCrashed) {
		return errCrashed.Error()
	}
	switch phrase, unreachable := adapter.Describe(err); {
	case unreachable:
		return "cannot reach " + host + ": " + phrase
	case phrase != "":
		return phrase
	}
	return "the check failed"
}

// safeProbe calls a.Probe, recovering a panic into an error reading ("adapter crashed") plus a
// log entry carrying the stack — a crashing adapter must only affect its own service's card,
// never the poller itself. The API key (inside t) is never part of the log entry.
func (p *Poller) safeProbe(ctx context.Context, a adapter.Adapter, t adapter.Target, kind shape.Kind) (data any, err error) {
	defer func() {
		if r := recover(); r != nil {
			p.cfg.Log.Error("adapter panicked during probe",
				"kind", kind, "panic", r, "stack", string(debug.Stack()))
			data, err = nil, errCrashed
		}
	}()
	return a.Probe(ctx, t, kind)
}
