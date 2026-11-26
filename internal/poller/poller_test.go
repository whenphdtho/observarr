package poller

import (
	"context"
	"log/slog"
	"net/url"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
	"observarr/internal/state"
)

// fakeAdapter is a minimal adapter.Adapter for testing the poller without any real app.
type fakeAdapter struct {
	info         adapter.Info
	delay        time.Duration                             // if >0, Probe waits this long (or until ctx is done) before answering
	ignoreCtxFor time.Duration                             // if >0, Probe ignores ctx cancellation for this long (simulates a slow-to-notice call)
	probe        func(kind shape.Kind, n int) (any, error) // optional; n is the 1-based call number for kind

	mu    sync.Mutex
	calls map[shape.Kind][]time.Time

	current atomic.Int32 // number of Probe calls currently in flight, across all kinds
	peak    atomic.Int32 // highest current has ever reached; >1 means two loops probed at once
}

func newFakeAdapter(typ string, kinds ...shape.Kind) *fakeAdapter {
	return &fakeAdapter{
		info:  adapter.Info{Type: typ, Name: typ, Kinds: kinds},
		calls: make(map[shape.Kind][]time.Time),
	}
}

func (f *fakeAdapter) Info() adapter.Info { return f.info }

func (f *fakeAdapter) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	return adapter.Identity{}, nil
}

func (f *fakeAdapter) Probe(ctx context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	n := f.current.Add(1)
	defer f.current.Add(-1)
	for {
		peak := f.peak.Load()
		if n <= peak || f.peak.CompareAndSwap(peak, n) {
			break
		}
	}

	f.mu.Lock()
	f.calls[kind] = append(f.calls[kind], time.Now())
	callN := len(f.calls[kind])
	fn := f.probe
	f.mu.Unlock()

	if f.ignoreCtxFor > 0 {
		// Simulates a real call that doesn't notice cancellation instantly (e.g. blocked in a
		// syscall): sleep for real, ignoring ctx, then check it.
		time.Sleep(f.ignoreCtxFor)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fn != nil {
		return fn(kind, callN)
	}
	return shape.Health{Version: "1.0", Warnings: []shape.Warning{}}, nil
}

func (f *fakeAdapter) callCount(kind shape.Kind) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls[kind])
}

func (f *fakeAdapter) callTimes(kind shape.Kind) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.calls[kind]...)
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", s, err)
	}
	return u
}

// eventually polls cond until it returns true or the timeout elapses, failing the test if it
// never does.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func testHub(id int64, typ string, kinds []shape.Kind) *state.Hub {
	h := state.NewHub()
	h.Put(state.ServiceState{ID: id, Type: typ, Name: typ, URL: "http://x", Kinds: kinds})
	return h
}

func TestFirstProbeImmediate(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: time.Hour},
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	eventually(t, time.Second, func() bool { return fa.callCount(shape.KindHealth) >= 1 })
}

func TestRepeatedProbes(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 20 * time.Millisecond},
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	eventually(t, time.Second, func() bool { return fa.callCount(shape.KindHealth) >= 5 })
}

func TestProbeTimeoutProducesErrorReading(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	fa.delay = time.Second // longer than the probe timeout below
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: time.Hour},
		Timeout:   20 * time.Millisecond,
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	eventually(t, time.Second, func() bool {
		snap := hub.Snapshot()
		r, ok := snap[0].Readings[shape.KindHealth]
		return ok && r.Error != ""
	})
}

func TestPanicRecovered(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	fa.probe = func(kind shape.Kind, n int) (any, error) {
		if n == 1 {
			panic("boom")
		}
		return shape.Health{Version: "1.0", Warnings: []shape.Warning{}}, nil
	}
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Log:       slog.New(slog.NewTextHandler(discard{}, nil)),
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 20 * time.Millisecond},
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	// The panicking first probe must have been recorded as an error, never crashing the loop.
	eventually(t, time.Second, func() bool {
		snap := hub.Snapshot()
		r, ok := snap[0].Readings[shape.KindHealth]
		return ok && r.Error == "adapter crashed"
	})
	// The loop must continue afterwards: later probes succeed.
	eventually(t, time.Second, func() bool {
		snap := hub.Snapshot()
		r, ok := snap[0].Readings[shape.KindHealth]
		return ok && r.Error == "" && r.Data != nil
	})
	if fa.callCount(shape.KindHealth) < 2 {
		t.Fatalf("callCount = %d, want at least 2 (panic must not stop the loop)", fa.callCount(shape.KindHealth))
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestBackoffDoublesAndResets(t *testing.T) {
	const base = 20 * time.Millisecond
	const failN = 4 // number of consecutive failures before success

	fa := newFakeAdapter("fake", shape.KindHealth)
	fa.probe = func(kind shape.Kind, n int) (any, error) {
		if n <= failN {
			return nil, errBoom
		}
		return shape.Health{Version: "1.0", Warnings: []shape.Warning{}}, nil
	}
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: base},
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	// Wait for two successful calls (call failN+1 and failN+2), so we can see the interval
	// reset: the wait *before* the first success still reflects failN consecutive failures
	// (the backoff that got it there); only the wait *after* a success is back to normal.
	eventually(t, 2*time.Second, func() bool { return fa.callCount(shape.KindHealth) >= failN+3 })

	times := fa.callTimes(shape.KindHealth)
	if len(times) < failN+3 {
		t.Fatalf("not enough calls recorded: %d", len(times))
	}

	// Gaps between consecutive failing calls must grow (doubling, with jitter).
	var prevGap time.Duration
	for i := 1; i < failN; i++ {
		gap := times[i].Sub(times[i-1])
		if i > 1 && gap < prevGap+prevGap/4 {
			t.Errorf("gap %d (%s) did not grow enough from previous gap (%s); backoff should double", i, gap, prevGap)
		}
		prevGap = gap
	}

	// The gap between the first and second successful calls must be back down near the base
	// interval — clearly smaller than the backed-off gap that led into the first success.
	lastBackedOffGap := times[failN].Sub(times[failN-1]) // last failure -> first success
	resetGap := times[failN+1].Sub(times[failN])         // first success -> second success
	if resetGap >= lastBackedOffGap {
		t.Errorf("resetGap (%s) should be smaller than the backed-off gap (%s) — back-off should reset on success", resetGap, lastBackedOffGap)
	}
}

var errBoom = &testError{"boom"}

type testError struct{ s string }

func (e *testError) Error() string { return e.s }

func TestIdleWhileNoWatchersFastWhenSubscribed(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindQueue)
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindQueue: 20 * time.Millisecond},
		Idle:      1200 * time.Millisecond,
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	// No watchers: after the immediate first probe, calls must stay rare for a while. Margin
	// widened generously (idle 1200ms vs. a 300ms window) so a loaded CI machine running the
	// active-mode goroutine late still can't produce more than a handful of calls here.
	time.Sleep(300 * time.Millisecond)
	before := fa.callCount(shape.KindQueue)
	if before > 4 {
		t.Fatalf("callCount while idle = %d, want a handful at most (idle interval should dominate)", before)
	}

	// A watcher appears: the idle kind must speed up. Give it generous slack beyond the ~1s
	// re-check tick, and only require a couple more calls (not a specific rate) so timing noise
	// on a slow machine doesn't flake this.
	_, watcherCancel := hub.Subscribe()
	defer watcherCancel()

	eventually(t, 2*time.Second, func() bool { return fa.callCount(shape.KindQueue) >= before+2 })
}

func TestStartTwiceReplacesRunningLoop(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	fa.delay = 15 * time.Millisecond // gives any accidental second loop a window to overlap in
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 20 * time.Millisecond},
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")}
	p.Start(ctx, svc)
	time.Sleep(20 * time.Millisecond)
	p.Start(ctx, svc) // replace while running

	time.Sleep(300 * time.Millisecond)
	p.Stop(1)

	// The structural guarantee ("at most one loop ever runs per service") rather than a call
	// rate: no probe for this service should ever have been in flight while another one was,
	// regardless of how fast or slow the machine running the test is.
	if peak := fa.peak.Load(); peak > 1 {
		t.Errorf("peak concurrent probes = %d, want 1 (two loops ran for the same service)", peak)
	}
	if got := fa.callCount(shape.KindHealth); got < 2 {
		t.Errorf("callCount = %d, want at least a couple of calls (the loop must still have run)", got)
	}
}

func TestStopWaits(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 15 * time.Millisecond},
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	eventually(t, time.Second, func() bool { return fa.callCount(shape.KindHealth) >= 2 })
	p.Stop(1)
	after := fa.callCount(shape.KindHealth)
	time.Sleep(150 * time.Millisecond)
	if fa.callCount(shape.KindHealth) != after {
		t.Errorf("probes continued after Stop returned: %d -> %d", after, fa.callCount(shape.KindHealth))
	}
}

func TestCloseStopsAll(t *testing.T) {
	fa1 := newFakeAdapter("fake1", shape.KindHealth)
	fa2 := newFakeAdapter("fake2", shape.KindHealth)
	reg := adapter.NewRegistry()
	reg.Register(fa1)
	reg.Register(fa2)
	hub := state.NewHub()
	hub.Put(state.ServiceState{ID: 1, Type: "fake1", Kinds: fa1.info.Kinds})
	hub.Put(state.ServiceState{ID: 2, Type: "fake2", Kinds: fa2.info.Kinds})

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 15 * time.Millisecond},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake1", BaseURL: mustURL(t, "http://x")})
	p.Start(ctx, Service{ID: 2, Type: "fake2", BaseURL: mustURL(t, "http://x")})

	eventually(t, time.Second, func() bool {
		return fa1.callCount(shape.KindHealth) >= 2 && fa2.callCount(shape.KindHealth) >= 2
	})
	p.Close()
	c1, c2 := fa1.callCount(shape.KindHealth), fa2.callCount(shape.KindHealth)
	time.Sleep(150 * time.Millisecond)
	if fa1.callCount(shape.KindHealth) != c1 || fa2.callCount(shape.KindHealth) != c2 {
		t.Errorf("probes continued after Close returned")
	}
}

// TestCloseWaitsDespiteConcurrentStop guards against Close returning while a loop that a
// concurrent Stop had already (but not yet fully) removed is still running: Stop and Close race
// on the same id, and the adapter is slow to notice cancellation, widening the window in which a
// buggy Close could return early. When Close returns, no probe must still be in flight.
func TestCloseWaitsDespiteConcurrentStop(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	fa.ignoreCtxFor = 50 * time.Millisecond
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: time.Hour},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})

	eventually(t, time.Second, func() bool { return fa.current.Load() >= 1 }) // probe in flight

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); p.Stop(1) }()
	go func() { defer wg.Done(); p.Close() }()
	wg.Wait()

	if got := fa.current.Load(); got != 0 {
		t.Fatalf("in-flight probes after Close returned = %d, want 0 (Close must wait for every probe, even one a concurrent Stop raced on)", got)
	}
}

func TestUnknownAdapterTypeRecordsErrorAndExits(t *testing.T) {
	reg := adapter.NewRegistry() // nothing registered
	hub := state.NewHub()
	hub.Put(state.ServiceState{ID: 1, Type: "ghost", Kinds: []shape.Kind{shape.KindHealth}})

	p := New(Config{Registry: reg, Hub: hub})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx, Service{ID: 1, Type: "ghost", BaseURL: mustURL(t, "http://x")})

	eventually(t, time.Second, func() bool {
		snap := hub.Snapshot()
		r, ok := snap[0].Readings[shape.KindHealth]
		return ok && r.Error == "unknown app type"
	})

	// The loop must have exited; Stop must return immediately rather than hang.
	done := make(chan struct{})
	go func() {
		p.Stop(1)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop did not return for an already-exited loop")
	}
}

func TestReplaceDoesNotRecordCancellationAsDown(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	fa.delay = 300 * time.Millisecond // long enough that the old loop's probe is still in
	// flight when we replace it below, and long enough that the replaced loop's own fresh
	// probe — which must still complete normally — gives this test time to observe it.
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	events, cancelSub := hub.Subscribe()
	defer cancelSub()

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: time.Hour}, // only the first probe matters
		Timeout:   2 * time.Second,                                           // must not fire before the replace does
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")}

	p.Start(ctx, svc)
	time.Sleep(30 * time.Millisecond) // let the first probe start and block in fa.Probe
	p.Start(ctx, svc)                 // replace while that probe is still in flight

	// No event for this service must ever carry a down/error health reading: the old probe's
	// context.Canceled (caused only by the replace) must be swallowed, not recorded. We know we
	// waited long enough once we see the replaced loop's own probe succeed.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Service == nil {
				continue
			}
			r, ok := ev.Service.Readings[shape.KindHealth]
			if !ok {
				continue
			}
			if r.Error != "" {
				t.Fatalf("unexpected error reading after replace: %q (must not surface the replace's own cancellation)", r.Error)
			}
			// A successful reading: the replaced loop's fresh probe completed normally.
			if fa.callCount(shape.KindHealth) < 2 {
				t.Fatalf("callCount = %d, want at least 2 (one cancelled, one from the new loop)", fa.callCount(shape.KindHealth))
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for the replaced loop's probe to complete")
		}
	}
}

func TestBackoffIntervalZeroFailuresUnchanged(t *testing.T) {
	p := New(Config{
		Registry:  adapter.NewRegistry(),
		Hub:       state.NewHub(),
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 7 * time.Second},
	})
	if got := p.backoffInterval(shape.KindHealth, 0); got != 7*time.Second {
		t.Errorf("backoffInterval(health, 0) = %s, want 7s (base interval unchanged)", got)
	}
}

func TestBackoffIntervalCapsAtFiveMinutesForSmallBase(t *testing.T) {
	p := New(Config{
		Registry:  adapter.NewRegistry(),
		Hub:       state.NewHub(),
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: time.Second},
	})
	if got := p.backoffInterval(shape.KindHealth, 20); got != 5*time.Minute {
		t.Errorf("backoffInterval(health, 20) = %s, want 5m", got)
	}
}

func TestBackoffIntervalNeverFasterThanConfiguredIdle(t *testing.T) {
	// No watchers, so the queue kind's base interval is Idle (10m) — far above the 5m default
	// back-off ceiling. Back-off must never make a kind faster than its configured interval, so
	// even with many consecutive failures the interval must stay at 10m, not drop to 5m.
	p := New(Config{
		Registry:  adapter.NewRegistry(),
		Hub:       state.NewHub(),
		Idle:      10 * time.Minute,
		Intervals: map[shape.Kind]time.Duration{shape.KindQueue: time.Second},
	})
	if got := p.backoffInterval(shape.KindQueue, 50); got != 10*time.Minute {
		t.Errorf("backoffInterval(queue, 50) = %s, want 10m (never faster than the configured interval)", got)
	}
}

func TestConcurrentStartStopSameID(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth)
	fa.delay = 3 * time.Millisecond // small, deterministic window to catch overlapping probes
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 5 * time.Millisecond},
	})
	defer p.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc := Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					p.Start(ctx, svc)
				}
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					p.Stop(svc.ID)
				}
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()

	if peak := fa.peak.Load(); peak > 1 {
		t.Fatalf("peak concurrent probes = %d, want 1 (two loops ran for the same service)", peak)
	}

	// Whatever state the churn left it in, a final Stop must bring it to a complete halt.
	p.Stop(svc.ID)
	after := fa.callCount(shape.KindHealth)
	time.Sleep(50 * time.Millisecond)
	if fa.callCount(shape.KindHealth) != after {
		t.Errorf("a probe fired after the final Stop returned")
	}
}

func TestNoGoroutineLeak(t *testing.T) {
	fa := newFakeAdapter("fake", shape.KindHealth, shape.KindQueue)
	reg := adapter.NewRegistry()
	reg.Register(fa)
	hub := testHub(1, "fake", fa.info.Kinds)

	p := New(Config{
		Registry:  reg,
		Hub:       hub,
		Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 10 * time.Millisecond, shape.KindQueue: 10 * time.Millisecond},
	})

	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	for i := 0; i < 5; i++ {
		p.Start(ctx, Service{ID: 1, Type: "fake", BaseURL: mustURL(t, "http://x")})
		time.Sleep(20 * time.Millisecond)
		p.Stop(1)
	}
	cancel()
	p.Close()

	eventually(t, 2*time.Second, func() bool {
		return runtime.NumGoroutine() <= baseline+2 // small slack for the runtime/test harness itself
	})
}

// TestStuckAndCrashingServicesDoNotStallOthers runs a service whose app never answers and one whose adapter always
// panics next to a healthy one: the healthy one keeps being probed, the others show an error, and Close ends all
// of them at once, leaving no goroutine behind.
func TestStuckAndCrashingServicesDoNotStallOthers(t *testing.T) {
	stuck := newFakeAdapter("stuck", shape.KindHealth, shape.KindQueue)
	stuck.delay = time.Hour
	crash := newFakeAdapter("crash", shape.KindHealth)
	crash.probe = func(shape.Kind, int) (any, error) { panic("boom") }
	ok := newFakeAdapter("ok", shape.KindHealth)
	reg := adapter.NewRegistry()
	hub := state.NewHub()
	for i, fa := range []*fakeAdapter{stuck, crash, ok} {
		reg.Register(fa)
		hub.Put(state.ServiceState{ID: int64(i + 1), Type: fa.info.Type, Name: fa.info.Type, Kinds: fa.info.Kinds})
	}
	baseline := runtime.NumGoroutine()
	p := New(Config{Registry: reg, Hub: hub, Log: slog.New(slog.NewTextHandler(discard{}, nil)),
		Timeout: 50 * time.Millisecond, Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 10 * time.Millisecond}})
	for i, typ := range []string{"stuck", "crash", "ok"} {
		p.Start(context.Background(), Service{ID: int64(i + 1), Type: typ, BaseURL: mustURL(t, "http://x")})
	}

	eventually(t, 2*time.Second, func() bool { return ok.callCount(shape.KindHealth) >= 20 })
	errorOf := func(id int64) string {
		for _, s := range hub.Snapshot() {
			if s.ID == id {
				return s.Readings[shape.KindHealth].Error
			}
		}
		return ""
	}
	eventually(t, 2*time.Second, func() bool { return errorOf(1) != "" && errorOf(2) == "adapter crashed" })
	if e := errorOf(3); e != "" {
		t.Errorf("healthy service shows %q", e)
	}

	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return within a second")
	}
	eventually(t, 2*time.Second, func() bool { return runtime.NumGoroutine() <= baseline+2 })
}
