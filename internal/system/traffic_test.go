package system

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"observarr/internal/collect"
	"observarr/internal/store"
)

// zone is where the tests' days begin: an hour ahead of UTC.
var zone = time.FixedZone("test", 3600)

// memStore keeps daily totals in memory; fail makes writes fail.
type memStore struct {
	mu   sync.Mutex
	rows map[[2]string]pair[uint64]
	fail bool
}

func (m *memStore) AddTraffic(_ context.Context, rows []store.TrafficRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("disk full")
	}
	if m.rows == nil {
		m.rows = map[[2]string]pair[uint64]{}
	}
	for _, r := range rows {
		k := [2]string{r.Day, r.Scope}
		m.rows[k] = pair[uint64]{m.rows[k][0] + r.Rx, m.rows[k][1] + r.Tx}
	}
	return nil
}

func (m *memStore) TrafficDay(_ context.Context, day string) ([]store.TrafficRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.TrafficRow
	for k, b := range m.rows {
		if k[0] == day {
			out = append(out, store.TrafficRow{Day: day, Scope: k[1], Rx: b[0], Tx: b[1]})
		}
	}
	return out, nil
}

func (m *memStore) TrafficSum(_ context.Context, scope, from, to string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n uint64
	for k, b := range m.rows {
		if k[1] == scope && k[0] >= from && k[0] < to {
			n += b[0] + b[1]
		}
	}
	return n, nil
}

func (m *memStore) get(day, scope string) pair[uint64] {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rows[[2]string{day, scope}]
}

func eth(rx, tx uint64) []collect.NetIf {
	return []collect.NetIf{{Name: "eth0", RxBytes: rx, TxBytes: tx}}
}

func at(day, clock string) time.Time {
	t, err := time.ParseInLocation(time.DateOnly+" 15:04:05", day+" "+clock, zone)
	if err != nil {
		panic(err)
	}
	return t
}

func TestTrafficSpreadsBytesOverMinutes(t *testing.T) {
	tr := NewTraffic(zone, "day", 1e9)
	tr.observe(at("2026-11-23", "10:00:30"), eth(0, 0), nil)
	// 6000 bytes down over a minute, half of it in 10:00 and half in 10:01.
	tr.observe(at("2026-11-23", "10:01:30"), eth(6000, 600), nil)

	s := tr.Summary()
	if s.TodayRx != 6000 || s.TodayTx != 600 || s.PeakRx != 100 || s.PeakTx != 10 {
		t.Fatalf("summary = %+v, want 6000/600 today and a peak of 100/10 B/s (the reading)", s)
	}
	if s.Budget == nil || *s.Budget != (Budget{Period: "day", Limit: 1e9, Used: 6600}) {
		t.Fatalf("budget = %+v", s.Budget)
	}
	v := tr.View("24h", tr.last)
	n := len(v.Times)
	if n != minuteSlots || v.Step != 60 || v.Times[n-1] != at("2026-11-23", "10:00:00").UnixMilli() ||
		v.Host.Series == nil || v.Host.Series.Rx[n-1] != 50 || v.Host.Rx != 100 || v.Host.Tx != 10 {
		t.Fatalf("24h view = %d times ending %v, %+v", n, v.Times[n-1], v.Host)
	}
	if rx := v.Host.Series.Rx[n-2]; !math.IsNaN(rx) {
		t.Fatalf("a minute before the first reading = %v, want a gap", rx)
	}
	live := tr.View("5m", tr.last)
	if live.Range != "5m" || len(live.Times) != 2 || live.Host.Series.Rx[1] != 100 {
		t.Fatalf("5m view = %+v %+v", live.Times, live.Host.Series)
	}
}

func TestTrafficPeakRightAfterStart(t *testing.T) {
	tr := NewTraffic(zone, "", 0)
	tr.observe(at("2026-11-23", "10:00:00"), eth(0, 0), nil)
	tr.observe(at("2026-11-23", "10:00:02"), eth(1_550_000, 20_000), nil) // 6.2 Mbit/s down, no minute closed yet
	if s := tr.Summary(); s.PeakRx != 775_000 || s.PeakTx != 10_000 {
		t.Fatalf("peak = %v/%v, want the reading's 775000/10000 B/s", s.PeakRx, s.PeakTx)
	}
	tr.observe(at("2026-11-23", "10:00:04"), eth(1_560_000, 20_000), nil) // slower now: the peak stays
	if s := tr.Summary(); s.PeakRx != 775_000 {
		t.Fatalf("peak after a slower reading = %v", s.PeakRx)
	}
	tr.observe(at("2026-11-24", "00:00:01"), eth(1_560_000, 20_000), nil) // too long after to count
	tr.observe(at("2026-11-24", "00:00:03"), eth(1_560_200, 20_000), nil)
	if s := tr.Summary(); s.PeakRx != 100 {
		t.Fatalf("peak on a new day = %v, want only its own 100 B/s", s.PeakRx)
	}
}

func TestTrafficCounterResetsCountNothing(t *testing.T) {
	tr := NewTraffic(zone, "", 0)
	now := at("2026-11-23", "10:00:00")
	app := func(pid int, rx uint64) []netCounters { return []netCounters{{name: "app", pid: pid, rx: rx}} }
	step := func(d time.Duration, ifs []collect.NetIf, ctrs []netCounters) {
		now = now.Add(d)
		tr.observe(now, ifs, ctrs)
	}
	step(0, eth(5000, 0), app(10, 9000))
	step(2*time.Second, eth(10, 0), app(10, 10))        // host rebooted, container counters reset
	step(2*time.Second, eth(20, 0), app(11, 1_000_000)) // a restarted container: new counters
	step(2*time.Second, append(eth(30, 0), collect.NetIf{Name: "eth1", RxBytes: 1 << 40}), app(11, 1_000_100))
	step(2*maxGap, eth(1<<30, 0), app(11, 1<<30)) // a gap too long to trust

	s := tr.Summary()
	v := tr.View("5m", tr.last)
	if s.TodayRx != 10+10 || len(v.Containers) != 1 || v.Containers[0].TodayRx != 100 {
		t.Fatalf("today = %d host, %+v; want only the increases (20 host, 100 container)", s.TodayRx, v.Containers)
	}
	if v.Host.Rx != 0 || v.Containers[0].Rx != 0 {
		t.Fatalf("rates after a long gap = %v %v", v.Host.Rx, v.Containers[0].Rx)
	}
}

func TestTrafficDayRolloverAndMonthlyBudget(t *testing.T) {
	st := &memStore{}
	st.AddTraffic(context.Background(), []store.TrafficRow{
		{Day: "2026-11-01", Scope: hostScope, Rx: 700, Tx: 300},
		{Day: "2026-11-30", Scope: hostScope, Rx: 40, Tx: 2},
		{Day: "2026-11-30", Scope: ctrScope + "app", Rx: 5, Tx: 1},
		{Day: "2026-10-31", Scope: hostScope, Rx: 1e6},
	})
	tr := NewTraffic(zone, "month", 1e12)
	if err := tr.load(context.Background(), st, at("2026-11-30", "23:58:00")); err != nil {
		t.Fatal(err)
	}
	if s := tr.Summary(); s.TodayRx != 40 || s.Budget.Used != 1000+42 {
		t.Fatalf("after loading = %+v %+v", s, s.Budget)
	}

	tr.observe(at("2026-11-30", "23:59:00"), eth(0, 0), nil)
	tr.observe(at("2026-12-01", "00:01:00"), eth(1200, 0), nil) // 600 on each day
	if s := tr.Summary(); s.TodayRx != 600 || s.PeakRx != 10 || s.Budget.Used != 600 {
		t.Fatalf("a new month = %+v %+v; want only its 600 bytes, 10 B/s at most", s, s.Budget)
	}
	tr.observe(at("2026-12-01", "23:59:00"), eth(1200, 0), nil) // too long after the last reading to count
	tr.observe(at("2026-12-02", "00:00:30"), eth(1500, 0), nil) // 200 on the 1st, 100 on the 2nd
	if s := tr.Summary(); s.TodayRx != 100 || s.Budget.Used != 600+200+100 {
		t.Fatalf("the next day of the month: used %d, want 900", s.Budget.Used)
	}

	if err := tr.persist(context.Background(), st, false); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		day  string
		want uint64
	}{{"2026-11-30", 40 + 600}, {"2026-12-01", 600 + 200}, {"2026-12-02", 0}} {
		if got := st.get(c.day, hostScope)[0]; got != c.want {
			t.Errorf("%s: %d bytes written, want %d", c.day, got, c.want)
		}
	}
}

func TestTrafficPersistKeepsWhatFailed(t *testing.T) {
	st := &memStore{fail: true}
	tr := NewTraffic(zone, "", 0)
	tr.observe(at("2026-11-23", "10:00:00"), eth(0, 0), []netCounters{{name: "app", pid: 1}})
	tr.observe(at("2026-11-23", "10:01:00"), eth(100, 10), []netCounters{{name: "app", pid: 1, rx: 7}})
	tr.observe(at("2026-11-23", "10:01:30"), eth(400, 10), nil)
	if err := tr.persist(context.Background(), st, false); err == nil {
		t.Fatal("a failed write reported no error")
	}
	st.fail = false
	if err := tr.persist(context.Background(), st, false); err != nil {
		t.Fatal(err)
	}
	if got := st.get("2026-11-23", hostScope); got != (pair[uint64]{100, 10}) {
		t.Fatalf("host = %v, want the closed minute's 100/10", got)
	}
	if got := st.get("2026-11-23", ctrScope+"app"); got != (pair[uint64]{7, 0}) {
		t.Fatalf("app = %v", got)
	}
	// On shutdown the open minute counts too.
	if err := tr.persist(context.Background(), st, true); err != nil {
		t.Fatal(err)
	}
	if got := st.get("2026-11-23", hostScope); got != (pair[uint64]{400, 10}) {
		t.Fatalf("host after the final write = %v, want 400/10", got)
	}
}

func TestTrafficIsBounded(t *testing.T) {
	tr := NewTraffic(zone, "", 0)
	now := at("2026-11-23", "00:00:00")
	var ctrs []netCounters
	for i := range maxTracked + 50 {
		ctrs = append(ctrs, netCounters{name: fmt.Sprintf("c%03d", i), pid: 1})
	}
	tr.observe(now, eth(0, 0), ctrs)
	if n := len(tr.flows); n != maxTracked+1 {
		t.Fatalf("%d flows, want the host and %d containers", n, maxTracked)
	}
	// Two days of readings once a minute, of the host and five containers that send a byte a second.
	ctrs = ctrs[:5]
	for m := 1; m < 2*minuteSlots; m++ {
		for i := range ctrs {
			ctrs[i].tx = uint64(m * 60)
		}
		tr.observe(now.Add(time.Duration(m)*time.Minute), eth(uint64(m)*6000, 0), ctrs)
	}
	if n := len(tr.flows); n != 1+len(ctrs) {
		t.Fatalf("%d flows, want the host and the %d containers seen in the last day", n, len(ctrs))
	}
	v := tr.View("24h", tr.last)
	if len(v.Times) != minuteSlots || len(v.Host.Series.Rx) != len(v.Times) || len(v.Containers) != len(ctrs) {
		t.Fatalf("24h view: %d times, %d values, %d containers", len(v.Times), len(v.Host.Series.Rx), len(v.Containers))
	}
	if first, last := v.Times[0], v.Times[len(v.Times)-1]; last-first >= 24*time.Hour.Milliseconds() {
		t.Fatalf("24h view spans %v", time.Duration(last-first)*time.Millisecond)
	}
	if r := v.Host.Series.Rx[len(v.Times)/2]; r != 100 || v.Containers[0].Series.Tx[3] != 1 {
		t.Fatalf("averages = %v host, %v container; want 100 and 1", r, v.Containers[0].Series.Tx[3])
	}
	if live := tr.View("5m", tr.last); len(live.Times) != 5 {
		t.Fatalf("5m view holds %d readings, want the 5 of the last five minutes", len(live.Times))
	}
}

func TestSamplerCountsTraffic(t *testing.T) {
	f := &fake{
		host: collect.Host{Net: eth(1000, 0)},
		cs: []collect.Container{
			{ID: "a", Name: "app", State: "running", PID: 10, NetworkMode: "bridge"},
			{ID: "b", Name: "web", State: "running", PID: 20, NetworkMode: "host"},
		},
		usage: []collect.ContainerUsage{{ID: "a", NetRx: 100}, {ID: "b", NetRx: 100}},
	}
	s, clk, _ := newTest(f, 1)
	st := &memStore{}
	s.KeepTraffic(st, NewTraffic(zone, "day", 5000))
	ctx := context.Background()
	s.list(ctx)
	s.step(ctx)
	clk.add(2 * time.Second)
	f.mu.Lock()
	f.host.Net = eth(3000, 0)
	f.usage = []collect.ContainerUsage{{ID: "a", NetRx: 300}, {ID: "b", NetRx: 900}}
	f.mu.Unlock()
	s.step(ctx)

	snap := s.Snapshot()
	if snap.Traffic == nil || snap.Traffic.TodayRx != 2000 || snap.Traffic.Budget.Used != 2000 {
		t.Fatalf("snapshot traffic = %+v", snap.Traffic)
	}
	v, _ := s.Network(ctx, "5m")
	if v.Host.Rx != 1000 || len(v.Containers) != 1 || v.Containers[0].Name != "app" || v.Containers[0].Rx != 100 ||
		v.Containers[0].TodayRx != 200 {
		t.Fatalf("network = %+v %+v; want the host and app only (web has the host's network)", v.Host, v.Containers)
	}

	// Without host figures there is no summary.
	f.mu.Lock()
	f.hostErr = collect.ErrUnavailable
	f.mu.Unlock()
	s.step(ctx)
	if s.Snapshot().Traffic != nil {
		t.Fatal("traffic summary without host figures")
	}
}

func TestRunLoadsAndWritesTraffic(t *testing.T) {
	today := time.Now().In(zone).Format(time.DateOnly)
	st := &memStore{}
	st.AddTraffic(context.Background(), []store.TrafficRow{{Day: today, Scope: hostScope, Rx: 12345}})
	f := &fake{host: collect.Host{Net: eth(0, 0)}}
	s, _, _ := newTest(f, 1)
	fast(s)
	s.KeepTraffic(st, NewTraffic(zone, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor := func(rx uint64) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if tr := s.Snapshot().Traffic; tr != nil && tr.TodayRx == rx {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("today = %+v, want %d bytes down", s.Snapshot().Traffic, rx)
			}
		}
	}
	waitFor(12345)
	f.mu.Lock()
	f.host.Net = eth(5000, 0)
	f.mu.Unlock()
	waitFor(12345 + 5000)
	cancel()
	<-done
	// Stopping writes what was counted, though the minute had not ended.
	if got := st.get(today, hostScope)[0]; got != 12345+5000 {
		t.Fatalf("written = %d, want %d", got, 12345+5000)
	}
}
