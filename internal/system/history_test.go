package system

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"slices"
	"testing"
	"time"
	_ "time/tzdata" // Europe/Berlin wherever the tests run

	"observarr/internal/collect"
	"observarr/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "observarr.db"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// addMinute writes one minute in which the host received rx bytes (and sent half that), with the containers' rows.
func addMinute(t *testing.T, st *store.Store, loc *time.Location, ts int64, rx uint64, host *store.HostAvg,
	ctrs ...store.SourceBytes) {
	t.Helper()
	m := store.HistoryMinute{Minute: ts, Hour: hourStart(ts, loc), Day: dayStart(ts, loc), Host: host,
		Traffic: append([]store.SourceBytes{{Source: hostScope, Rx: rx, Tx: rx / 2}}, ctrs...)}
	if err := st.AddHistory(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

// readMinutes takes a reading at the start of each minute from `from` up to (not including) `to`, of a host that receives
// 60 bytes and sends 6 a minute at 25% CPU and 1000 bytes of memory, writing the history every hour.
func readMinutes(t *testing.T, tr *Traffic, st HistoryStore, from, to time.Time) {
	t.Helper()
	rx := uint64(0)
	for now, n := from, 0; now.Before(to); now, n = now.Add(time.Minute), n+1 {
		tr.observe(now, eth(rx, rx/10), nil)
		tr.addHost(25, 1000)
		rx += 60
		if n%60 == 59 {
			if err := tr.persistHistory(context.Background(), st); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tr.persistHistory(context.Background(), st); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryRollsUpLocalHoursAndDaysAcrossDST(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		day   string
		hours int
	}{{"2026-03-29", 23}, {"2026-10-25", 25}} {
		t.Run(c.day, func(t *testing.T) {
			st := openStore(t)
			tr := NewTraffic(berlin, "", 0)
			tr.keep = true
			midnight, _ := time.ParseInLocation(time.DateOnly, c.day, berlin)
			next := midnight.AddDate(0, 0, 1)
			if got := next.Sub(midnight); got != time.Duration(c.hours)*time.Hour {
				t.Fatalf("%s lasts %v", c.day, got)
			}
			// From an hour before the day to an hour after it; the reading at 01:00 the next day closes 00:59.
			readMinutes(t, tr, st, midnight.Add(-time.Hour), next.Add(time.Hour+time.Minute))
			ctx := context.Background()
			minutes, _ := st.TrafficSeries(ctx, store.ResMinute, midnight.Unix(), next.Unix())
			hours, _ := st.TrafficSeries(ctx, store.ResHour, midnight.Unix(), next.Unix())
			days, _ := st.TrafficSeries(ctx, store.ResDay, midnight.Add(-25*time.Hour).Unix(), next.Add(2*time.Hour).Unix())
			if len(minutes) != c.hours*60 {
				t.Fatalf("%d minute buckets, want %d", len(minutes), c.hours*60)
			}
			var sum uint64
			for _, m := range minutes {
				if m.Rx != 60 || m.Tx != 6 || m.N != 1 {
					t.Fatalf("minute %v", m)
				}
				sum += m.Rx
			}
			if len(hours) != c.hours {
				t.Fatalf("%d hour buckets on the day, want %d", len(hours), c.hours)
			}
			for i, h := range hours {
				if h.Rx != 3600 || h.Tx != 360 || h.N != 60 || (i > 0 && h.TS-hours[i-1].TS != 3600) {
					t.Fatalf("hour %d = %+v", i, h)
				}
			}
			if len(days) != 3 || days[1].TS != midnight.Unix() || days[1].Rx != sum || days[1].N != int64(c.hours*60) ||
				days[0].Rx != 3600 || days[2].Rx != 3600 {
				t.Fatalf("days = %+v, want the day's %d bytes in its own bucket and an hour on each side", days, sum)
			}
			host, _ := st.HostSeries(ctx, store.ResDay, midnight.Unix(), next.Unix())
			if len(host) != 1 || host[0].CPU != 25 || host[0].Mem != 1000 || host[0].N != int64(c.hours*60) {
				t.Fatalf("host day = %+v", host)
			}

			// The year's days are local days: the one that changes the clocks is not 24 hours long.
			v, err := tr.longView(ctx, st, "1y", next.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			n := len(v.Times)
			if n != 366 || v.Times[n-1] != next.UnixMilli() || v.Times[n-1]-v.Times[n-2] != int64(c.hours)*3_600_000 {
				t.Fatalf("1y times end %v, %v", time.UnixMilli(v.Times[n-2]), time.UnixMilli(v.Times[n-1]))
			}
			if r := v.Host.Series.Rx[n-2]; r != 1 || !math.IsNaN(v.Host.Series.Rx[n-4]) {
				t.Fatalf("1y rates = %v, %v; want 1 B/s on the day and a gap before", r, v.Host.Series.Rx[n-4])
			}
		})
	}
}

func TestRangesStepsAndPoints(t *testing.T) {
	st := openStore(t)
	tr := NewTraffic(zone, "", 0)
	now := at("2026-11-23", "10:30:00")
	hour := at("2026-11-23", "10:00:00").Unix()
	// Two hours with traffic, and a gap between them.
	addMinute(t, st, zone, hour, 3600, &store.HostAvg{CPU: 40, Mem: 2048}, store.SourceBytes{Source: ctrScope + "app", Rx: 600})
	addMinute(t, st, zone, hour-2*3600, 3600, nil)
	tr.observe(now, eth(0, 0), nil)
	ctx := context.Background()

	for _, c := range []struct {
		rng    string
		step   int64
		points int
		last   int64 // seconds
	}{
		{"24h", 60, 1440, minuteOf(now)*60 - 60},
		{"7d", 3600, 168, hour},
		{"30d", 3600, 720, hour},
		{"1y", 86400, 366, at("2026-11-23", "00:00:00").Unix()},
	} {
		var v TrafficView
		var err error
		if c.rng == "24h" {
			v = tr.View(c.rng)
		} else if v, err = tr.longView(ctx, st, c.rng, now); err != nil {
			t.Fatal(err)
		}
		n := len(v.Times)
		if v.Range != c.rng || v.Step != c.step || n != c.points || v.Times[n-1] != c.last*1000 {
			t.Fatalf("%s: range %s, step %d, %d times ending %v", c.rng, v.Range, v.Step, n, v.Times[n-1])
		}
		for i := 1; i < n; i++ {
			if v.Times[i]-v.Times[i-1] != c.step*1000 {
				t.Fatalf("%s: times %d and %d are %d ms apart", c.rng, i-1, i, v.Times[i]-v.Times[i-1])
			}
		}
		h, err := tr.hostHistory(ctx, st, c.rng, now)
		if err != nil || h.Step != c.step || len(h.Times) != n || h.Times[n-1] != v.Times[n-1] || len(h.CPU) != n ||
			len(h.MemUsed) != n {
			t.Fatalf("%s host: %v step %d, %d times", c.rng, err, h.Step, len(h.Times))
		}
		if c.rng == "24h" {
			continue
		}
		// One minute was watched in each bucket with traffic: 3600 bytes in it are 60 B/s.
		rx := v.Host.Series.Rx
		if c.rng != "1y" && (rx[n-1] != 60 || !math.IsNaN(rx[n-2]) || rx[n-3] != 60 || !math.IsNaN(rx[0])) {
			t.Fatalf("%s: rates %v %v %v, first %v", c.rng, rx[n-3], rx[n-2], rx[n-1], rx[0])
		}
		if c.rng == "1y" && (rx[n-1] != 60 || !math.IsNaN(rx[n-2])) {
			t.Fatalf("1y: rates %v %v", rx[n-2], rx[n-1])
		}
		// The app's 600 bytes are 10 B/s over its hour's watched minute; the day watched two minutes.
		want := map[bool]float64{false: 10, true: 5}[c.rng == "1y"]
		if len(v.Containers) != 1 || v.Containers[0].Name != "app" || v.Containers[0].Series.Rx[n-1] != want {
			t.Fatalf("%s: containers %+v", c.rng, v.Containers)
		}
		if h.CPU[n-1] != 40 || h.MemUsed[n-1] != 2048 || !math.IsNaN(h.CPU[n-2]) {
			t.Fatalf("%s host: %v %v", c.rng, h.CPU[n-2:], h.MemUsed[n-1])
		}
	}
}

func TestPreviousPeriodTotals(t *testing.T) {
	st := openStore(t)
	tr := NewTraffic(zone, "", 0)
	now := at("2026-11-23", "10:30:00")
	ctx := context.Background()

	// Five minutes: the five minutes before the last five, from memory.
	readMinutes(t, tr, st, now.Add(-15*time.Minute), now.Add(time.Second))
	v := tr.View("5m")
	if p, err := tr.previous(ctx, st, "5m", v); err != nil || p == nil || *p != (Totals{300, 30}) {
		t.Fatalf("5m previous = %+v %v, want 5 minutes of 60/6", p, err)
	}

	// A day: the day before, from minutes.
	v = tr.View("24h")
	first := v.Times[0] / 1000
	if p, _ := tr.previous(ctx, st, "24h", v); p != nil {
		t.Fatalf("24h previous without a kept day before = %+v", p)
	}
	addMinute(t, st, zone, first-86400, 1000, nil)
	addMinute(t, st, zone, first-60, 500, nil)
	if p, err := tr.previous(ctx, st, "24h", v); err != nil || p == nil || *p != (Totals{1500, 750}) {
		t.Fatalf("24h previous = %+v %v", p, err)
	}

	// A week: the week before, from hours.
	if v, _ = tr.longView(ctx, st, "7d", now); v.Times[0]/1000 != hourStart(now.Unix(), zone)-167*3600 {
		t.Fatalf("7d starts %v", time.UnixMilli(v.Times[0]))
	}
	if p, _ := tr.previous(ctx, st, "7d", v); p != nil {
		t.Fatalf("7d previous, the history starting within it = %+v", p)
	}
	week := v.Times[0]/1000 - 7*86400
	addMinute(t, st, zone, week, 7000, nil)
	addMinute(t, st, zone, week+3*86400, 3000, nil)
	if p, err := tr.previous(ctx, st, "7d", v); err != nil || p == nil || p.In != 7000+3000 {
		t.Fatalf("7d previous = %+v %v", p, err)
	}

	// Thirty days: the thirty local days before, from days.
	v, _ = tr.longView(ctx, st, "30d", now)
	day := addDays(v.Times[0]/1000, -30, zone)
	addMinute(t, st, zone, day+3600, 100_000, nil)
	if p, err := tr.previous(ctx, st, "30d", v); err != nil || p == nil || p.In != 100_000 {
		t.Fatalf("30d previous = %+v %v", p, err)
	}
	// A year: days are not kept two years, so the year before is never known in full.
	v, _ = tr.longView(ctx, st, "1y", now)
	if p, err := tr.previous(ctx, st, "1y", v); err != nil || p != nil {
		t.Fatalf("1y previous = %+v %v", p, err)
	}
}

func TestDailyHasThirtyDaysTodayLast(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if err := st.AddTraffic(ctx, []store.TrafficRow{
		{Day: "2026-11-22", Scope: hostScope, Rx: 10, Tx: 1},
		{Day: "2026-10-25", Scope: hostScope, Rx: 20, Tx: 2},
		{Day: "2026-10-24", Scope: hostScope, Rx: 30, Tx: 3}, // 31 days ago
		{Day: "2026-11-22", Scope: ctrScope + "app", Rx: 99},
		{Day: "2026-11-23", Scope: hostScope, Rx: 1000},
	}); err != nil {
		t.Fatal(err)
	}
	tr := NewTraffic(zone, "", 0)
	now := at("2026-11-23", "10:30:00")
	if err := tr.load(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	tr.observe(now, eth(0, 0), nil)
	tr.observe(now.Add(time.Minute), eth(500, 50), nil)
	d, err := tr.daily(ctx, st, now)
	if err != nil || len(d) != 30 {
		t.Fatalf("daily = %d days, %v", len(d), err)
	}
	if d[0].Day != "2026-10-25" || *d[0].In != 20 || d[28].Day != "2026-11-22" || *d[28].In != 10 || *d[28].Out != 1 ||
		d[29].Day != "2026-11-23" || *d[29].In != 1500 || *d[29].Out != 50 || d[1].In != nil || d[1].Out != nil {
		t.Fatalf("daily = %v %v ... %v %v", d[0], d[1], d[28], d[29])
	}
}

func TestSparkIsTheLastHour(t *testing.T) {
	tr := NewTraffic(zone, "", 0)
	start := at("2026-11-23", "08:00:00")
	if s := tr.spark(start); len(s.Spark.Times) != sparkMinutes || s.PreviousCPU != nil || s.PreviousMem != nil {
		t.Fatalf("spark before any reading = %d times, %v %v", len(s.Spark.Times), s.PreviousCPU, s.PreviousMem)
	}
	for k := range 130 {
		tr.observe(start.Add(time.Duration(k)*time.Minute), eth(0, 0), nil)
		tr.addHost(float64(k), uint64(1000*k))
	}
	// Minutes 0..128 are closed: the spark holds 69..128, the hour before 9..68.
	s := tr.spark(start)
	sp := s.Spark
	if sp.Step != 60 || len(sp.Times) != sparkMinutes || len(sp.CPU) != sparkMinutes || len(sp.MemUsed) != sparkMinutes ||
		sp.CPU[0] != 69 || sp.CPU[59] != 128 || sp.MemUsed[59] != 128_000 ||
		sp.Times[59] != start.Add(128*time.Minute).UnixMilli() {
		t.Fatalf("spark = %+v", sp)
	}
	if s.PreviousCPU == nil || *s.PreviousCPU != 38.5 || *s.PreviousMem != 38_500 {
		t.Fatalf("previous = %v %v", s.PreviousCPU, s.PreviousMem)
	}
}

func TestRestartRestoresTheLastDay(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	start := at("2026-11-23", "08:00:00")
	before := NewTraffic(zone, "", 0)
	before.keep = true
	now := start
	for k := range 90 {
		now = start.Add(time.Duration(k) * time.Minute)
		before.observe(now, eth(uint64(k)*600, 0), []netCounters{{name: "app", pid: 1, rx: uint64(k) * 60}})
		before.addHost(12.5, 4096)
	}
	if err := before.persistHistory(ctx, st); err != nil {
		t.Fatal(err)
	}
	old := before.View("24h")

	after := NewTraffic(zone, "", 0)
	after.keep = true
	restart := now.Add(30 * time.Second) // in the minute that was open
	if err := after.restore(ctx, st, restart); err != nil {
		t.Fatal(err)
	}
	after.observe(restart, eth(0, 0), nil)
	v := after.View("24h")
	if len(v.Times) != minuteSlots || v.Times[minuteSlots-1] != old.Times[minuteSlots-1] {
		t.Fatalf("restored view: %d times", len(v.Times))
	}
	held := 0
	for i := range v.Times {
		a, b := old.Host.Series.Rx[i], v.Host.Series.Rx[i]
		if math.IsNaN(a) != math.IsNaN(b) || (!math.IsNaN(a) && a != b) {
			t.Fatalf("minute %d: %v before the restart, %v after", i, a, b)
		}
		if !math.IsNaN(b) {
			held++
		}
	}
	if held != 89 || len(v.Containers) != 1 || v.Containers[0].Series == nil ||
		v.Containers[0].Series.Rx[minuteSlots-1] != 1 {
		t.Fatalf("%d minutes held, containers %+v", held, v.Containers)
	}
	s := after.spark(restart)
	if s.Spark.CPU[sparkMinutes-1] != 12.5 || s.Spark.MemUsed[sparkMinutes-1] != 4096 || *s.PreviousCPU != 12.5 {
		t.Fatalf("restored spark = %+v %v", s.Spark.CPU[sparkMinutes-1], s.PreviousCPU)
	}
}

// failing is a HistoryStore whose writes fail.
type failing struct {
	HistoryStore
	writes int
}

func (f *failing) AddHistory(context.Context, store.HistoryMinute) error {
	f.writes++
	return errors.New("database is locked")
}

func TestHistoryQueueIsBoundedWhileWritesFail(t *testing.T) {
	tr := NewTraffic(zone, "", 0)
	tr.keep = true
	f := &failing{}
	start := at("2026-11-23", "08:00:00")
	for k := range 3 * maxQueued {
		tr.observe(start.Add(time.Duration(k)*time.Minute), eth(0, 0), nil)
		if k%60 == 0 {
			if err := tr.persistHistory(context.Background(), f); err == nil && k > 0 {
				t.Fatal("a failed write reported no error")
			}
		}
	}
	tr.mu.Lock()
	n, first := len(tr.queue), tr.queue[0].Minute
	tr.mu.Unlock()
	if n != maxQueued || first != start.Add(time.Duration(3*maxQueued-1-maxQueued)*time.Minute).Unix() {
		t.Fatalf("queue holds %d minutes from %v, want the latest %d", n, time.Unix(first, 0), maxQueued)
	}
	if f.writes > 6 {
		t.Fatalf("%d writes tried, want one per call", f.writes)
	}
	st := openStore(t)
	if err := tr.persistHistory(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.TrafficSeries(context.Background(), store.ResMinute, 0, math.MaxInt64)
	if len(rows) != maxQueued || len(tr.queue) != 0 {
		t.Fatalf("%d minutes written, %d left", len(rows), len(tr.queue))
	}
}

func TestRunRestoresAndServesTheHistory(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	ago := (time.Now().Unix()/60 - 10) * 60 // a minute ten minutes ago, before a restart
	addMinute(t, st, zone, ago, 6000, &store.HostAvg{CPU: 50, Mem: 77}, store.SourceBytes{Source: ctrScope + "app", Rx: 60})
	f := &fake{host: collect.Host{Net: eth(0, 0)}}
	s, _, _ := newTest(f, 1)
	fast(s)
	s.KeepTraffic(st, NewTraffic(zone, "", 0))
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()
	for deadline := time.Now().Add(5 * time.Second); s.Snapshot().Host == nil; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no sample")
		}
	}

	v, err := s.Network(ctx, "24h")
	if err != nil || len(v.Times) != minuteSlots || len(v.Daily) != dailyDays {
		t.Fatalf("24h = %d times, %d days, %v", len(v.Times), len(v.Daily), err)
	}
	i := slices.Index(v.Times, ago*1000)
	if i < 0 || v.Host.Series.Rx[i] != 100 || len(v.Containers) != 1 || v.Containers[0].Series.Rx[i] != 1 {
		t.Fatalf("the restored minute (at %d) = %+v %+v", i, v.Host, v.Containers)
	}
	if sp := s.Spark(); len(sp.Spark.Times) != sparkMinutes || sp.Spark.CPU[slices.Index(sp.Spark.Times, ago*1000)] != 50 {
		t.Fatalf("spark = %v", sp.Spark.CPU)
	}
	for _, rng := range []string{"7d", "30d", "1y"} {
		v, err := s.Network(ctx, rng)
		bucket := hourStart(ago, zone)
		if rng == "1y" {
			bucket = dayStart(ago, zone)
		}
		i := slices.Index(v.Times, bucket*1000)
		if err != nil || len(v.Times) != spans[rng].points || i < 0 || v.Host.Series == nil || v.Host.Series.Rx[i] != 100 {
			t.Fatalf("%s = %d times, bucket at %d, %v", rng, len(v.Times), i, err)
		}
		h, err := s.HostHistory(ctx, rng)
		if err != nil || h.CPU[i] != 50 {
			t.Fatalf("%s host = %v %v", rng, h.CPU[i], err)
		}
	}
	if h, err := s.HostHistory(ctx, "5m"); err != nil || len(h.Times) == 0 || len(h.CPU) != len(h.Times) {
		t.Fatalf("5m host = %+v %v", h, err)
	}
}
