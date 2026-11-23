// Package system samples the host and its containers through a collect.Collector: it turns cumulative counters
// into rates, keeps a short history of host readings and hands the latest snapshot to subscribers.
package system

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"observarr/internal/collect"
	"observarr/internal/collect/cgroup"
	"observarr/internal/collect/docker"
)

const (
	watchedEvery  = 2 * time.Second  // sample interval while someone watches live events
	idleEvery     = 30 * time.Second // sample interval otherwise
	listEvery     = 30 * time.Second // container list refresh, besides refreshes on Docker events
	minList       = 5 * time.Second  // shortest time between two refreshes
	settle        = time.Second      // Docker events are collected this long before the list is refreshed
	minBackoff    = time.Second      // first wait before reconnecting the Docker events stream
	maxBackoff    = time.Minute      // longest wait; a stream that lasted this long resets the wait
	historyWindow = 5 * time.Minute
	maxHistory    = 300         // host readings kept at most; event-driven samples can come faster than every 2 s
	persistEvery  = time.Minute // daily traffic totals are written this often
	persistWait   = time.Second // longest wait for the last write when stopping
)

// Notices shown instead of figures a source cannot provide. Error text is never shown: it goes to the log.
const (
	noticeHost        = "Host stats: mount the host /proc and set OBSERVARR_HOST_PROC"
	noticeHostFailed  = "Host stats could not be read; see the log"
	NoticeDocker      = "Containers: set OBSERVARR_DOCKER_HOST (socket proxy recommended)"
	NoticeDockerError = "Containers: Docker could not be read; see the log"
	noticeInspect     = "The socket proxy does not allow container inspection (set CONTAINERS=1)"
	noticeEvents      = "Container health may lag: the socket proxy does not allow /events (set EVENTS=1)"
	noticeTooMany     = "Containers: only the first 1000 are shown"
	noticeUsage       = "Container CPU and memory: mount the host /sys/fs/cgroup and set OBSERVARR_HOST_CGROUP"
	noticeUsageFailed = "Container CPU and memory could not be read; see the log"
	noticeV1          = "Container CPU and memory need cgroup v2"
	noticeCgroupNS    = "Container CPU and memory need `cgroup: host` on the Observarr container"
)

// HostView is one host reading with rates.
type HostView struct {
	CPUPercent float64        `json:"cpuPercent"` // of all CPUs together, 0-100
	CPUs       int            `json:"cpus"`
	Load       [3]float64     `json:"load"`
	MemUsed    uint64         `json:"memUsed"` // bytes; total minus available
	MemTotal   uint64         `json:"memTotal"`
	SwapUsed   uint64         `json:"swapUsed"`
	SwapTotal  uint64         `json:"swapTotal"`
	Uptime     time.Duration  `json:"uptimeNs"`
	Disks      []collect.Disk `json:"disks"`
	Net        []NetRate      `json:"net"`
	At         time.Time      `json:"at"`
}

// NetRate is one interface's traffic in bytes per second.
type NetRate struct {
	Name string  `json:"name"`
	Rx   float64 `json:"rx"`
	Tx   float64 `json:"tx"`
}

// ContainerView is one container with its usage. Usage figures are zero when they could not be read; rates are also
// zero right after start or a counter reset, and network rates always are for a container without a network of its
// own (see collect.Container.OwnNetwork).
type ContainerView struct {
	collect.Container
	CPUPercent   float64 `json:"cpuPercent"` // one core = 100
	MemBytes     uint64  `json:"memBytes"`
	MemLimit     uint64  `json:"memLimit"` // 0 = unlimited
	BlkReadRate  float64 `json:"blkReadRate"`
	BlkWriteRate float64 `json:"blkWriteRate"`
	NetRxRate    float64 `json:"netRxRate"`
	NetTxRate    float64 `json:"netTxRate"`
}

// Snapshot is the latest sample. Host is nil when the host cannot be read. A Snapshot is shared between all readers
// and must not be modified.
type Snapshot struct {
	Host       *HostView       `json:"host"`
	Containers []ContainerView `json:"containers"` // sorted by name
	Notices    []string        `json:"notices"`
	Traffic    *TrafficSummary `json:"traffic"` // nil when the host cannot be read
	At         time.Time       `json:"at"`
}

type hostReading struct {
	h  collect.Host
	at time.Time
}

type usageReading struct {
	u   collect.ContainerUsage
	pid int
	at  time.Time
}

// Sampler reads its collector periodically: every 2 s while someone watches (watchers() > 0), every 30 s otherwise.
// The container list is refreshed every 30 s and shortly after Docker events, but at most every 5 s.
type Sampler struct {
	c        collect.Collector
	watchers func() int
	log      *slog.Logger
	now      func() time.Time
	traffic  *Traffic
	store    TrafficStore // where daily traffic totals are kept; nil = nowhere

	watchedEvery, idleEvery, listEvery, minList, settle, minBackoff, maxBackoff, persistEvery time.Duration // the constants; tests shorten them

	changed      chan struct{} // a Docker event arrived
	eventsDenied atomic.Bool   // the socket proxy refused the events stream at the last attempt
	panicked     atomic.Bool

	// Owned by Run's goroutine.
	sampledAt time.Time
	prevHost  *hostReading
	prevUsage map[string]usageReading

	lmu         sync.Mutex // guards the fields below, which the list's goroutine and Run's share
	containers  []collect.Container
	listNotices []string
	logged      map[string]string // last error logged per source, so a lasting failure is logged once

	mu      sync.Mutex
	snap    Snapshot
	history []HostView
	subs    map[chan Snapshot]struct{}
}

// New returns a Sampler over c. watchers reports how many clients watch live (the hub's Watchers). It counts
// traffic in memory only, with days in the local time zone and no budget; see KeepTraffic.
func New(c collect.Collector, watchers func() int, log *slog.Logger) *Sampler {
	return &Sampler{c: c, watchers: watchers, log: log, now: time.Now, traffic: NewTraffic(time.Local, "", 0),
		watchedEvery: watchedEvery, idleEvery: idleEvery, listEvery: listEvery, minList: minList, settle: settle,
		minBackoff: minBackoff, maxBackoff: maxBackoff, persistEvery: persistEvery,
		changed: make(chan struct{}, 1), prevUsage: map[string]usageReading{}, logged: map[string]string{},
		snap: Snapshot{Containers: []ContainerView{}, Notices: []string{}}, subs: map[chan Snapshot]struct{}{}}
}

// KeepTraffic makes the sampler count traffic into t and keep its daily totals in st. Call it before Run.
func (s *Sampler) KeepTraffic(st TrafficStore, t *Traffic) {
	s.store, s.traffic = st, t
}

// Run samples until ctx ends. Call it once.
func (s *Sampler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	listed := make(chan struct{}, 1)
	wg.Go(func() { s.follow(ctx) })
	wg.Go(func() { s.lists(ctx, listed) })
	if s.store != nil {
		if err := s.traffic.load(ctx, s.store, s.now()); err != nil && ctx.Err() == nil {
			s.log.Warn("reading today's traffic failed; today counts from now", "err", err)
		}
		wg.Go(func() { s.persist(ctx) })
	}

	tick := time.NewTicker(s.watchedEvery)
	defer tick.Stop()
	s.step(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-listed:
		case <-tick.C:
			// Half a tick of slack: ticks drift against sampledAt, and 30 s must not turn into 32.
			if s.watchers() == 0 && s.now().Sub(s.sampledAt) < s.idleEvery-s.watchedEvery/2 {
				continue
			}
		}
		if ctx.Err() != nil {
			return
		}
		s.step(ctx)
	}
}

// follow streams Docker events, reconnecting with back-off, and tells Run about each one.
func (s *Sampler) follow(ctx context.Context) {
	wait, reconnect, lastErr := s.minBackoff, false, ""
	for {
		events, err := s.c.Events(ctx)
		s.eventsDenied.Store(errors.Is(err, docker.ErrDenied))
		switch {
		case errors.Is(err, collect.ErrUnavailable):
			return
		case err != nil:
			if msg := err.Error(); msg != lastErr && ctx.Err() == nil {
				s.log.Warn("Docker events unavailable; retrying", "err", err)
				lastErr = msg
			}
		default:
			lastErr = ""
			started := time.Now()
			if reconnect {
				s.notify() // whatever happened while the stream was down
			}
			for range events {
				s.notify()
			}
			if time.Since(started) >= s.maxBackoff {
				wait = s.minBackoff
			}
		}
		reconnect = true
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, s.maxBackoff)
	}
}

func (s *Sampler) notify() {
	select {
	case s.changed <- struct{}{}:
	default:
	}
}

// lists keeps the container list current, apart from sampling so a slow Docker proxy never holds up the host
// figures. It lists at start, every listEvery and settle after a Docker event, but at most once per minList, and
// asks Run for a sample after each refresh.
func (s *Sampler) lists(ctx context.Context, listed chan<- struct{}) {
	for {
		select { // events until now are covered by this refresh
		case <-s.changed:
		default:
		}
		s.list(ctx)
		last := time.Now()
		select {
		case listed <- struct{}{}:
		default:
		}
		timer := time.NewTimer(s.listEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-s.changed:
			timer.Stop()
			select {
			case <-ctx.Done():
				return
			case <-time.After(max(s.settle, s.minList-time.Since(last))):
			}
		}
	}
}

// persist writes daily traffic totals every minute, and once more when ctx ends.
func (s *Sampler) persist(ctx context.Context) {
	tick := time.NewTicker(s.persistEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			final, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistWait)
			defer cancel()
			if err := s.traffic.persist(final, s.store, true); err != nil {
				s.log.Warn("writing traffic totals failed", "err", err)
			}
			return
		case <-tick.C:
		}
		s.report(ctx, "traffic totals", s.traffic.persist(ctx, s.store, false))
	}
}

// recovered recovers a panic, logging the first one, so one bad reading never stops sampling.
func (s *Sampler) recovered() {
	if r := recover(); r != nil && !s.panicked.Swap(true) {
		s.log.Error("system sample failed", "panic", r, "stack", string(debug.Stack()))
	}
}

// step takes one sample with the latest container list.
func (s *Sampler) step(ctx context.Context) {
	defer s.recovered()
	now := s.now()
	s.sampledAt = now
	snap := Snapshot{At: now, Containers: []ContainerView{}, Notices: []string{}}
	snap.Host, snap.Notices = s.host(ctx, now)
	s.lmu.Lock()
	containers := s.containers
	snap.Notices = append(snap.Notices, s.listNotices...)
	s.lmu.Unlock()
	if s.eventsDenied.Load() {
		snap.Notices = append(snap.Notices, noticeEvents)
	}
	var notices []string
	var counters []netCounters
	snap.Containers, notices, counters = s.usage(ctx, now, containers)
	snap.Notices = append(snap.Notices, notices...)
	if ctx.Err() != nil {
		return // a sample cut short would show sources as failing
	}
	var ifs []collect.NetIf
	if snap.Host != nil {
		ifs = s.prevHost.h.Net // the reading host just took
	}
	s.traffic.observe(now, ifs, counters)
	if snap.Host != nil {
		sum := s.traffic.Summary()
		snap.Traffic = &sum
	}
	s.publish(snap)
}

// list refreshes the container list.
func (s *Sampler) list(ctx context.Context) {
	defer s.recovered()
	cs, err := s.c.Containers(ctx)
	var notices []string
	switch {
	case err == nil:
	case errors.Is(err, collect.ErrUnavailable):
		cs, notices = nil, []string{NoticeDocker}
	case errors.Is(err, docker.ErrTooMany) || errors.Is(err, docker.ErrInspect): // listed, in part or not inspected
		if errors.Is(err, docker.ErrTooMany) {
			notices = append(notices, noticeTooMany)
		}
		if errors.Is(err, docker.ErrInspect) { // without PID, health and restarts
			notices = append(notices, noticeInspect)
		}
	default:
		cs, notices = nil, []string{NoticeDockerError} // a stale list would show stopped containers as running
	}
	s.report(ctx, "containers", err)
	slices.SortFunc(cs, func(a, b collect.Container) int { return cmp.Compare(a.Name, b.Name) })
	s.lmu.Lock()
	s.containers, s.listNotices = cs, notices
	s.lmu.Unlock()
}

// host reads the host and computes rates against the previous reading. It returns the notices for the snapshot.
func (s *Sampler) host(ctx context.Context, now time.Time) (*HostView, []string) {
	h, err := s.c.Host(ctx)
	s.report(ctx, "host", err)
	if errors.Is(err, collect.ErrUnavailable) {
		return nil, []string{noticeHost}
	}
	if err != nil {
		return nil, []string{noticeHostFailed}
	}
	notices := []string{}
	for _, p := range h.StuckDisks {
		notices = append(notices, "Disk "+p+" is not responding")
	}
	v := &HostView{CPUs: h.CPUs, Load: h.Load, MemTotal: h.MemTotal, SwapTotal: h.SwapTotal,
		MemUsed: h.MemTotal - min(h.MemAvailable, h.MemTotal), SwapUsed: h.SwapTotal - min(h.SwapFree, h.SwapTotal),
		Uptime: h.Uptime, Disks: h.Disks, Net: make([]NetRate, 0, len(h.Net)), At: now}
	var prev map[string]collect.NetIf
	var dt float64
	if p := s.prevHost; p != nil {
		dt = now.Sub(p.at).Seconds()
		if h.CPUTotal > p.h.CPUTotal && h.CPUIdle >= p.h.CPUIdle {
			total, idle := h.CPUTotal-p.h.CPUTotal, h.CPUIdle-p.h.CPUIdle
			v.CPUPercent = 100 * float64(total-min(idle, total)) / float64(total)
		}
		prev = make(map[string]collect.NetIf, len(p.h.Net))
		for _, n := range p.h.Net {
			prev[n.Name] = n
		}
	}
	for _, n := range h.Net {
		r := NetRate{Name: n.Name}
		if p, ok := prev[n.Name]; ok {
			r.Rx, r.Tx = rate(n.RxBytes, p.RxBytes, dt), rate(n.TxBytes, p.TxBytes, dt)
		}
		v.Net = append(v.Net, r)
	}
	s.prevHost = &hostReading{h: h, at: now}
	return v, notices
}

// usage reads the listed containers' usage and computes rates against the previous readings. It also returns
// the network counters of the containers with a network of their own.
func (s *Sampler) usage(ctx context.Context, now time.Time, cs []collect.Container) ([]ContainerView, []string, []netCounters) {
	views := make([]ContainerView, len(cs))
	running := false
	for i, c := range cs {
		views[i].Container = c
		running = running || c.PID > 0
	}
	if !running {
		s.prevUsage = map[string]usageReading{}
		return views, nil, nil
	}
	us, err := s.c.Usage(ctx, cs)
	var notices []string
	switch {
	case errors.Is(err, collect.ErrUnavailable):
		notices = append(notices, noticeUsage)
	case err != nil:
		if errors.Is(err, cgroup.ErrV1) {
			notices = append(notices, noticeV1)
		}
		if errors.Is(err, cgroup.ErrCgroupNS) {
			notices = append(notices, noticeCgroupNS)
		}
		if notices == nil {
			notices = append(notices, noticeUsageFailed)
		}
	}
	s.report(ctx, "container usage", err)

	byID := make(map[string]collect.ContainerUsage, len(us))
	for _, u := range us {
		byID[u.ID] = u
	}
	next := make(map[string]usageReading, len(us))
	var counters []netCounters
	for i := range views {
		v := &views[i]
		u, ok := byID[v.ID]
		if !ok {
			continue
		}
		if v.OwnNetwork() && v.PID > 0 {
			counters = append(counters, netCounters{name: v.Name, pid: v.PID, rx: u.NetRx, tx: u.NetTx})
		}
		v.MemBytes, v.MemLimit = u.MemBytes, u.MemLimit
		if p, ok := s.prevUsage[v.ID]; ok && p.pid == v.PID { // another PID: restarted, the counters began anew
			dt := now.Sub(p.at).Seconds()
			v.CPUPercent = rate(u.CPUUsec, p.u.CPUUsec, dt) / 1e4 // µs per second → percent of one core
			v.BlkReadRate, v.BlkWriteRate = rate(u.BlkRead, p.u.BlkRead, dt), rate(u.BlkWrite, p.u.BlkWrite, dt)
			if v.OwnNetwork() {
				v.NetRxRate, v.NetTxRate = rate(u.NetRx, p.u.NetRx, dt), rate(u.NetTx, p.u.NetTx, dt)
			}
		}
		next[v.ID] = usageReading{u: u, pid: v.PID, at: now}
	}
	s.prevUsage = next
	return views, notices, counters
}

// rate is the per-second increase of a counter; none when the counter went back (reset) or no time passed.
func rate(cur, prev uint64, dt float64) float64 {
	if cur < prev || dt <= 0 {
		return 0
	}
	return float64(cur-prev) / dt
}

// report logs a source's error when it differs from the last one logged for that source. A source that is not
// configured is not an error, and nothing is logged while shutting down.
func (s *Sampler) report(ctx context.Context, source string, err error) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	if err == nil || errors.Is(err, collect.ErrUnavailable) || ctx.Err() != nil {
		delete(s.logged, source)
		return
	}
	if msg := err.Error(); s.logged[source] != msg {
		s.logged[source] = msg
		s.log.Warn("reading "+source+" failed", "err", err)
	}
}

// publish makes snap the current snapshot and hands it to every subscriber.
func (s *Sampler) publish(snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snap = snap
	if snap.Host != nil {
		s.history = append(s.history, *snap.Host)
		cut := snap.At.Add(-historyWindow)
		drop := 0
		for drop < len(s.history) && (!s.history[drop].At.After(cut) || len(s.history)-drop > maxHistory) {
			drop++
		}
		s.history = slices.Delete(s.history, 0, drop)
	}
	for ch := range s.subs {
		select { // replace an unread snapshot: subscribers only ever want the latest
		case <-ch:
		default:
		}
		ch <- snap // never blocks: only publish sends, under s.mu, into a buffer just emptied
	}
}

// Snapshot returns the latest sample.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

// History returns the host readings of the last five minutes, oldest first.
func (s *Sampler) History() []HostView {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := s.now().Add(-historyWindow)
	out := make([]HostView, 0, len(s.history))
	for _, v := range s.history {
		if v.At.After(cut) {
			out = append(out, v)
		}
	}
	return out
}

// Network returns the traffic over rng, "5m" or "24h".
func (s *Sampler) Network(rng string) TrafficView { return s.traffic.View(rng) }

// Subscribe returns a channel that holds the latest snapshot not yet received, and a cancel function (idempotent).
func (s *Sampler) Subscribe() (<-chan Snapshot, func()) {
	ch := make(chan Snapshot, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}
