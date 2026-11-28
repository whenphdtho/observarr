package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"observarr/internal/collect"
	"observarr/internal/collect/cgroup"
	"observarr/internal/collect/docker"
)

// fake is a scripted collector.
type fake struct {
	mu       sync.Mutex
	host     collect.Host
	hostErr  error
	cs       []collect.Container
	csErr    error
	usage    []collect.ContainerUsage
	usageErr error
	events   func(context.Context) (<-chan collect.Event, error)
	hosts    int
	lists    int
	panics   bool
	// Set, these make Host or Containers wait for their context's end, like a disk or a Docker proxy that
	// stopped answering.
	hostHangs, listHangs bool
}

// hang waits for ctx's end when set is.
func hang(ctx context.Context, set bool) error {
	if !set {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fake) Host(ctx context.Context) (collect.Host, error) {
	f.mu.Lock()
	hangs := f.hostHangs
	f.mu.Unlock()
	if err := hang(ctx, hangs); err != nil {
		return collect.Host{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hosts++
	if f.panics {
		panic("odd input")
	}
	h := f.host
	h.Net = slices.Clone(h.Net) // a real reading shares nothing with the previous one
	return h, f.hostErr
}

func (f *fake) Containers(ctx context.Context) ([]collect.Container, error) {
	f.mu.Lock()
	f.lists++
	hangs := f.listHangs
	f.mu.Unlock()
	if err := hang(ctx, hangs); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cs), f.csErr
}

func (f *fake) Usage(context.Context, []collect.Container) ([]collect.ContainerUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.usage), f.usageErr
}

func (f *fake) Events(ctx context.Context) (<-chan collect.Event, error) {
	f.mu.Lock()
	events := f.events
	f.mu.Unlock()
	if events == nil {
		return nil, collect.ErrUnavailable
	}
	return events(ctx)
}

func (f *fake) counts() (hosts, lists int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hosts, f.lists
}

// clock is a fake time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTest(f *fake, watchers int) (*Sampler, *clock, *bytes.Buffer) {
	var logs bytes.Buffer
	clk := &clock{t: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	s := New(f, func() int { return watchers }, slog.New(slog.NewTextHandler(&logs, nil)))
	s.now = clk.now
	return s, clk, &logs
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestRates(t *testing.T) {
	f := &fake{
		host: collect.Host{CPUTotal: 1000, CPUIdle: 800, CPUs: 4, MemTotal: 8 << 30, MemAvailable: 6 << 30,
			SwapTotal: 1 << 30, SwapFree: 1 << 30, Net: []collect.NetIf{{Name: "eth0", RxBytes: 1000, TxBytes: 500}}},
		cs: []collect.Container{
			{ID: "b", Name: "web", State: "running", PID: 20, NetworkMode: "host"},
			{ID: "a", Name: "app", State: "running", PID: 10, NetworkMode: "bridge"},
			{ID: "c", Name: "db", State: "exited"},
		},
		usage: []collect.ContainerUsage{
			{ID: "a", CPUUsec: 1_000_000, MemBytes: 100, MemLimit: 1000, BlkRead: 0, BlkWrite: 0, NetRx: 0, NetTx: 0},
			{ID: "b", CPUUsec: 0, NetRx: 0},
		},
	}
	s, clk, _ := newTest(f, 0)
	ctx := context.Background()
	s.list(ctx)
	s.step(ctx)
	first := s.Snapshot()
	if first.Host == nil || first.Host.CPUPercent != 0 || first.Host.Net[0].Rx != 0 {
		t.Fatalf("first sample has rates: %+v", first.Host)
	}
	if got := []string{first.Containers[0].Name, first.Containers[1].Name, first.Containers[2].Name}; !slices.Equal(got,
		[]string{"app", "db", "web"}) {
		t.Fatalf("containers not sorted by name: %v", got)
	}

	clk.add(2 * time.Second)
	f.mu.Lock()
	f.host.CPUTotal, f.host.CPUIdle = 1400, 1000 // 400 jiffies, 200 idle → 50 %
	f.host.Net[0].RxBytes, f.host.Net[0].TxBytes = 3000, 1500
	f.usage = []collect.ContainerUsage{
		{ID: "a", CPUUsec: 2_000_000, MemBytes: 200, MemLimit: 1000, BlkRead: 4096, BlkWrite: 8192, NetRx: 2000, NetTx: 1000},
		{ID: "b", CPUUsec: 500_000, NetRx: 1 << 20},
	}
	f.mu.Unlock()
	s.step(ctx)
	snap := s.Snapshot()
	h := snap.Host
	if !near(h.CPUPercent, 50) || h.MemUsed != 2<<30 || h.SwapUsed != 0 || h.CPUs != 4 {
		t.Fatalf("host = %+v", h)
	}
	if h.Net[0] != (NetRate{Name: "eth0", Rx: 1000, Tx: 500}) {
		t.Fatalf("net = %+v", h.Net)
	}
	app, db, web := snap.Containers[0], snap.Containers[1], snap.Containers[2]
	if !near(app.CPUPercent, 50) || app.MemBytes != 200 || app.MemLimit != 1000 || app.BlkReadRate != 2048 ||
		app.BlkWriteRate != 4096 || app.NetRxRate != 1000 || app.NetTxRate != 500 {
		t.Fatalf("app = %+v", app)
	}
	if db.CPUPercent != 0 || db.MemBytes != 0 {
		t.Fatalf("stopped container has usage: %+v", db)
	}
	if !near(web.CPUPercent, 25) || web.NetRxRate != 0 {
		t.Fatalf("host-network container = %+v; want CPU 25 and no network rate", web)
	}
	if len(snap.Notices) != 0 {
		t.Fatalf("notices = %v", snap.Notices)
	}
}

func TestCounterResetGivesNoRate(t *testing.T) {
	f := &fake{
		host:  collect.Host{CPUTotal: 1000, CPUIdle: 500, Net: []collect.NetIf{{Name: "eth0", RxBytes: 5000}}},
		cs:    []collect.Container{{ID: "a", Name: "app", State: "running", PID: 10}},
		usage: []collect.ContainerUsage{{ID: "a", CPUUsec: 9_000_000, NetRx: 9000}},
	}
	s, clk, _ := newTest(f, 0)
	s.list(context.Background())
	s.step(context.Background())
	clk.add(2 * time.Second)
	f.mu.Lock()
	f.host.CPUTotal, f.host.Net[0].RxBytes = 100, 10 // the host rebooted under us
	f.usage[0].CPUUsec, f.usage[0].NetRx = 1000, 10
	f.mu.Unlock()
	s.step(context.Background())
	snap := s.Snapshot()
	if snap.Host.CPUPercent != 0 || snap.Host.Net[0].Rx != 0 || snap.Containers[0].CPUPercent != 0 ||
		snap.Containers[0].NetRxRate != 0 {
		t.Fatalf("rates after a counter reset: %+v %+v", snap.Host, snap.Containers[0])
	}

	// A container restarted between two list refreshes has a new PID: its counters are not comparable.
	clk.add(2 * time.Second)
	f.mu.Lock()
	f.cs[0].PID = 11
	f.usage[0].CPUUsec = 5_000_000
	f.mu.Unlock()
	s.list(context.Background())
	s.step(context.Background())
	if c := s.Snapshot().Containers[0]; c.CPUPercent != 0 {
		t.Fatalf("rate across a restart: %+v", c)
	}
}

func TestNotices(t *testing.T) {
	secret := errors.New("dial unix /var/run/docker.sock: secret detail")
	cases := []struct {
		name string
		f    *fake
		want []string
	}{
		{"nothing configured", &fake{hostErr: collect.ErrUnavailable, csErr: collect.ErrUnavailable},
			[]string{noticeHost, NoticeDocker}},
		{"failures", &fake{hostErr: secret, csErr: secret}, []string{noticeHostFailed, NoticeDockerError}},
		{"no inspection", &fake{cs: []collect.Container{{ID: "a", Name: "app", State: "running"}},
			csErr: fmt.Errorf("%w: %w", docker.ErrInspect, secret)}, []string{noticeInspect}},
		{"no cgroup mount", &fake{cs: []collect.Container{{ID: "a", Name: "app", PID: 1}}, usageErr: collect.ErrUnavailable},
			[]string{noticeUsage}},
		{"cgroup v1 and namespace", &fake{cs: []collect.Container{{ID: "a", Name: "app", PID: 1}},
			usageErr: errors.Join(cgroup.ErrV1, cgroup.ErrCgroupNS)}, []string{noticeV1, noticeCgroupNS}},
		{"usage failure", &fake{cs: []collect.Container{{ID: "a", Name: "app", PID: 1}}, usageErr: secret},
			[]string{noticeUsageFailed}},
		{"too many containers", &fake{cs: []collect.Container{{ID: "a", Name: "app", State: "running"}},
			csErr: errors.Join(docker.ErrTooMany, fmt.Errorf("%w: %w", docker.ErrInspect, secret))},
			[]string{noticeTooMany, noticeInspect}},
		{"stuck disks", &fake{host: collect.Host{StuckDisks: []string{"/mnt/nas", "/mnt/usb"}}},
			[]string{"Disk /mnt/nas is not responding", "Disk /mnt/usb is not responding"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, logs := newTest(tc.f, 0)
			s.step(context.Background())
			s.list(context.Background())
			s.step(context.Background())
			snap := s.Snapshot()
			if !slices.Equal(snap.Notices, tc.want) {
				t.Fatalf("notices = %q, want %q", snap.Notices, tc.want)
			}
			if tc.name == "no inspection" && len(snap.Containers) != 1 {
				t.Fatal("the list without inspection was dropped")
			}
			if tc.f.hostErr == secret && strings.Count(logs.String(), "secret detail") != 2 {
				t.Fatalf("each failing source must be logged once:\n%s", logs)
			}
			if tc.f.hostErr == collect.ErrUnavailable && logs.Len() != 0 {
				t.Fatalf("an unconfigured source was logged:\n%s", logs)
			}
		})
	}
}

func TestHistoryWindow(t *testing.T) {
	s, clk, _ := newTest(&fake{}, 1)
	for range 200 { // 400 s at 2 s
		s.step(context.Background())
		clk.add(2 * time.Second)
	}
	h := s.History()
	if len(h) != 149 { // samples within the last 5 min, measured from now (2 s after the last sample)
		t.Fatalf("history has %d readings", len(h))
	}
	if !h[0].At.After(clk.now().Add(-historyWindow)) || !h[len(h)-1].At.Equal(clk.now().Add(-2*time.Second)) {
		t.Fatalf("history spans %v to %v", h[0].At, h[len(h)-1].At)
	}
	for range 2 * maxHistory { // event-driven samples, much faster than every 2 s
		s.step(context.Background())
		clk.add(100 * time.Millisecond)
	}
	if n := len(s.History()); n != maxHistory {
		t.Fatalf("history has %d readings, want the cap %d", n, maxHistory)
	}
}

func TestSubscriberGetsLatestOnly(t *testing.T) {
	s, clk, _ := newTest(&fake{}, 1)
	ch, cancel := s.Subscribe()
	for range 3 {
		clk.add(time.Second)
		s.step(context.Background())
	}
	got := <-ch
	if !got.At.Equal(clk.now()) {
		t.Fatalf("got the snapshot of %v, want the latest", got.At)
	}
	select {
	case <-ch:
		t.Fatal("more than the latest snapshot was queued")
	default:
	}
	cancel()
	cancel()
	s.step(context.Background())
	select {
	case <-ch:
		t.Fatal("a cancelled subscriber got a snapshot")
	default:
	}
}

func TestPanicRecoveredAndLoggedOnce(t *testing.T) {
	f := &fake{panics: true}
	s, _, logs := newTest(f, 0)
	s.step(context.Background())
	s.step(context.Background())
	if n := strings.Count(logs.String(), "system sample failed"); n != 1 {
		t.Fatalf("panic logged %d times:\n%s", n, logs)
	}
	f.mu.Lock()
	f.panics = false
	f.mu.Unlock()
	s.step(context.Background())
	if s.Snapshot().Host == nil {
		t.Fatal("sampling did not resume after a panic")
	}
}

// fast shortens the sampler's intervals for tests that run it.
func fast(s *Sampler) {
	s.watchedEvery, s.idleEvery, s.listEvery, s.settle = 10*time.Millisecond, 200*time.Millisecond, time.Hour,
		20*time.Millisecond
	s.minBackoff, s.maxBackoff, s.minList, s.minGap = 10*time.Millisecond, 40*time.Millisecond, 20*time.Millisecond,
		5*time.Millisecond
	s.now = time.Now
}

// Samples, whether a tick or a container list asks for them, come at least minGap apart.
func TestSamplesKeepMinGap(t *testing.T) {
	f := &fake{host: collect.Host{CPUTotal: 1000, CPUIdle: 800, CPUs: 4}}
	s, _, _ := newTest(f, 1)
	fast(s)
	s.listEvery, s.minList, s.minGap = 30*time.Millisecond, 30*time.Millisecond, 25*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	sub, stop := s.Subscribe()
	defer stop()
	go func() { s.Run(ctx); close(done) }()
	// Each sample says when it was taken, on the clock the sampler spaces them by: that time is compared, not the
	// moment this test got to see it. A sample this loop misses only widens a gap it then does not check.
	var at []time.Time
	for len(at) < 8 {
		select {
		case snap := <-sub:
			at = append(at, snap.At)
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d samples in ten seconds", len(at))
		}
	}
	cancel()
	<-done
	for i := 1; i < len(at); i++ {
		if gap := at[i].Sub(at[i-1]); gap < s.minGap {
			t.Fatalf("samples %d and %d taken %v apart, want at least minGap (%v)", i-1, i, gap, s.minGap)
		}
	}
}

// The first container list arrives as Run takes its first sample; the sample it asks for waits minGap, so the
// host's rates are measured over a real interval instead of reading as idle.
func TestSampleAfterListWaitsForCounters(t *testing.T) {
	f := &fake{host: collect.Host{CPUTotal: 1000, CPUIdle: 800, CPUs: 4}}
	s, _, _ := newTest(f, 0)
	fast(s)
	s.idleEvery, s.minGap = time.Hour, 150*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	start := time.Now()
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	var second time.Duration
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		f.mu.Lock()
		n := f.hosts
		f.mu.Unlock()
		if n >= 2 {
			second = time.Since(start)
			break
		}
	}
	if second == 0 {
		t.Fatal("no second sample after the first list")
	}
	if second < 140*time.Millisecond {
		t.Fatalf("second sample %v after start, want at least minGap (150ms) after the first", second)
	}
}

func TestListRefreshesAreRateLimited(t *testing.T) {
	events := make(chan collect.Event)
	f := &fake{events: func(context.Context) (<-chan collect.Event, error) { return events, nil }}
	s, _, _ := newTest(f, 0)
	fast(s)
	s.minList = 200 * time.Millisecond
	run(t, s)
	t.Cleanup(func() { close(events) })
	start := time.Now()
	for time.Since(start) < 500*time.Millisecond { // a container restarting over and over
		events <- collect.Event{ID: "a", Action: "restart"}
		time.Sleep(5 * time.Millisecond)
	}
	if _, lists := f.counts(); lists < 2 || lists > 4 { // at 0, 200 and 400 ms
		t.Fatalf("%d lists in 500 ms of events, want one per 200 ms", lists)
	}
}

func TestSlowListDoesNotHoldUpHostFigures(t *testing.T) {
	f := &fake{listHangs: true}
	s, _, _ := newTest(f, 1)
	fast(s)
	run(t, s)
	time.Sleep(200 * time.Millisecond)
	if hosts, _ := f.counts(); hosts < 10 {
		t.Fatalf("%d host samples in 200 ms while the container list hung, want about 20", hosts)
	}
	if s.Snapshot().Host == nil {
		t.Fatal("no host figures published")
	}
}

func run(t *testing.T, s *Sampler) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestIdleAndWatchedIntervals(t *testing.T) {
	idle, watched := &fake{}, &fake{}
	for _, tc := range []struct {
		f        *fake
		watchers int
	}{{idle, 0}, {watched, 1}} {
		s, _, _ := newTest(tc.f, tc.watchers)
		fast(s)
		run(t, s)
	}
	time.Sleep(300 * time.Millisecond)
	ih, _ := idle.counts()
	wh, _ := watched.counts()
	if ih < 2 || ih > 3 { // at start and after 200 ms
		t.Errorf("idle: %d samples in 300 ms, want 2-3", ih)
	}
	if wh < 10 {
		t.Errorf("watched: %d samples in 300 ms, want about 30", wh)
	}
}

func TestEventsRefreshTheList(t *testing.T) {
	events := make(chan collect.Event)
	f := &fake{events: func(context.Context) (<-chan collect.Event, error) { return events, nil }}
	s, _, _ := newTest(f, 0)
	fast(s)
	run(t, s)
	t.Cleanup(func() { close(events) }) // runs before run's cleanup waits for Run
	for _, lists := f.counts(); lists < 1; _, lists = f.counts() {
		time.Sleep(time.Millisecond) // the first list, at start, covers whatever happened before it
	}
	for range 5 { // a burst is one refresh
		events <- collect.Event{ID: "a", Action: "start"}
	}
	deadline := time.Now().Add(2 * time.Second)
	for _, lists := f.counts(); lists < 2; _, lists = f.counts() {
		if time.Now().After(deadline) {
			t.Fatal("an event did not refresh the container list")
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if _, lists := f.counts(); lists != 2 {
		t.Fatalf("%d lists for one start and one burst of events", lists)
	}
}

func TestEventsReconnectWithBackoff(t *testing.T) {
	var (
		mu    sync.Mutex
		calls []time.Time
	)
	f := &fake{events: func(context.Context) (<-chan collect.Event, error) {
		mu.Lock()
		calls = append(calls, time.Now())
		mu.Unlock()
		return nil, errors.New("connection refused")
	}}
	s, _, logs := newTest(f, 0)
	fast(s)
	run(t, s)
	time.Sleep(250 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 5 || len(calls) > 9 { // waits 10, 20, 40, 40, … ms
		t.Fatalf("%d connection attempts in 250 ms", len(calls))
	}
	for i, want := range []time.Duration{10, 20, 40, 40} {
		gap := calls[i+1].Sub(calls[i])
		if gap < want*time.Millisecond || gap > want*time.Millisecond+30*time.Millisecond {
			t.Errorf("wait %d = %v, want %v ms", i, gap, want)
		}
	}
	if n := strings.Count(logs.String(), "connection refused"); n != 1 {
		t.Errorf("the same events failure was logged %d times", n)
	}
}

func TestRunEndsWithContext(t *testing.T) {
	f := &fake{events: func(ctx context.Context) (<-chan collect.Event, error) {
		ch := make(chan collect.Event)
		go func() { <-ctx.Done(); close(ch) }()
		return ch, nil
	}}
	s, _, _ := newTest(f, 1)
	fast(s)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not end with its context")
	}
}

func TestRunEndsWhileAReadHangs(t *testing.T) {
	s, _, _ := newTest(&fake{hostHangs: true, listHangs: true}, 1)
	fast(s)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not end with its context while the host read hung")
	}
}

func TestEventsDeniedNotice(t *testing.T) {
	var deny atomic.Bool
	deny.Store(true)
	f := &fake{events: func(ctx context.Context) (<-chan collect.Event, error) {
		if deny.Load() {
			return nil, fmt.Errorf("docker: %w /events", docker.ErrDenied)
		}
		ch := make(chan collect.Event)
		go func() { <-ctx.Done(); close(ch) }()
		return ch, nil
	}}
	s, _, _ := newTest(f, 0)
	fast(s)
	run(t, s)
	wait := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for slices.Contains(s.Snapshot().Notices, noticeEvents) != want {
			if time.Now().After(deadline) {
				t.Fatalf("notices = %q, want the events notice: %v", s.Snapshot().Notices, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	wait(true)
	deny.Store(false)
	wait(false)
}
