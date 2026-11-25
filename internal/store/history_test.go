package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// minute returns a minute whose hour and day buckets are UTC's.
func minute(ts int64, rows ...SourceBytes) HistoryMinute {
	return HistoryMinute{Minute: ts, Hour: ts - ts%3600, Day: ts - ts%86400, Traffic: rows}
}

func TestCapSources(t *testing.T) {
	rows := []SourceBytes{{"host", 1000, 1000}}
	for i := range 100 {
		rows = append(rows, SourceBytes{fmt.Sprintf("container:c%03d", i), 10, 1})
	}
	got := CapSources(rows)
	if len(got) != MaxSources || got[0].Source != "host" || got[MaxSources-2].Source != "container:c061" ||
		got[MaxSources-1] != (SourceBytes{OtherSource, 38 * 10, 38}) {
		t.Fatalf("capped = %d rows: %v ... %v", len(got), got[:2], got[len(got)-2:])
	}
	if few := rows[:10]; len(CapSources(few)) != 10 {
		t.Fatal("a short list was changed")
	}
}

func TestHistoryRollsUpAndCapsEachBucket(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC).Unix()
	// The first minute brings 70 containers: 62 get a row of their own, 8 go to "other".
	rows := []SourceBytes{{"host", 5000, 500}}
	for i := range 70 {
		rows = append(rows, SourceBytes{fmt.Sprintf("container:a%02d", i), 10, 1})
	}
	m := minute(base, rows...)
	m.Host = &HostAvg{CPU: 10, Mem: 1000}
	if err := s.AddHistory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// The next minute brings a container the hour has no room for, and one it already holds.
	m = minute(base+60, SourceBytes{"host", 100, 10}, SourceBytes{"container:new", 7, 7},
		SourceBytes{"container:a00", 3, 3})
	m.Host = &HostAvg{CPU: 30, Mem: 3000}
	if err := s.AddHistory(ctx, m); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		res, ts   int64
		rows      int
		other     SourceBytes
		host, a00 SeriesRow
	}{
		{ResMinute, base, MaxSources, SourceBytes{OtherSource, 80, 8},
			SeriesRow{base, "host", 5000, 500, 1}, SeriesRow{base, "container:a00", 10, 1, 1}},
		{ResHour, base, MaxSources, SourceBytes{OtherSource, 87, 15},
			SeriesRow{base, "host", 5100, 510, 2}, SeriesRow{base, "container:a00", 13, 4, 2}},
		{ResDay, base - base%86400, MaxSources, SourceBytes{OtherSource, 87, 15},
			SeriesRow{base - base%86400, "host", 5100, 510, 2}, SeriesRow{base - base%86400, "container:a00", 13, 4, 2}},
	} {
		got, err := s.TrafficSeries(ctx, c.res, c.ts, c.ts+1)
		if err != nil {
			t.Fatal(err)
		}
		by := map[string]SeriesRow{}
		for _, r := range got {
			by[r.Source] = r
		}
		o := by[OtherSource]
		if len(got) != c.rows || o.Rx != c.other.Rx || o.Tx != c.other.Tx || by["host"] != c.host ||
			by["container:a00"] != c.a00 {
			t.Errorf("res %d: %d rows, other %+v, host %+v, a00 %+v", c.res, len(got), o, by["host"], by["container:a00"])
		}
	}
	// The second minute's own bucket has room for the new container.
	if got, _ := s.TrafficSeries(ctx, ResMinute, base+60, base+61); len(got) != 3 {
		t.Fatalf("second minute = %v", got)
	}

	host, err := s.HostSeries(ctx, ResHour, base, base+1)
	if err != nil || len(host) != 1 || host[0] != (HostRow{base, 20, 2000, 2}) {
		t.Fatalf("host hour = %v %v, want the averages of both minutes", host, err)
	}
}

func TestHistoryPrunes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{time.Hour, 47 * time.Hour, 49 * time.Hour, 34 * 24 * time.Hour,
		36 * 24 * time.Hour, 799 * 24 * time.Hour, 801 * 24 * time.Hour} {
		ts := now.Add(-age).Unix()
		m := HistoryMinute{Minute: ts, Hour: ts, Day: ts, Traffic: []SourceBytes{{"host", 1, 1}},
			Host: &HostAvg{CPU: 1, Mem: 1}}
		if err := s.AddHistory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PruneHistory(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		res  int64
		want int
	}{{ResMinute, 2}, {ResHour, 4}, {ResDay, 6}} {
		rows, _ := s.TrafficSeries(ctx, c.res, 0, now.Unix())
		host, _ := s.HostSeries(ctx, c.res, 0, now.Unix())
		if len(rows) != c.want || len(host) != c.want {
			t.Errorf("res %d: %d traffic and %d host buckets kept, want %d", c.res, len(rows), len(host), c.want)
		}
	}
}

func TestHostTrafficNeedsMostOfThePeriod(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	base := int64(1_800_000_000) // a whole hour
	if _, _, ok, err := s.HostTraffic(ctx, ResMinute, base, base+600); ok || err != nil {
		t.Fatalf("an empty history = %v %v", ok, err)
	}
	// Twelve minutes, base to base+11 min, each 10/1 bytes for the host and 99 for a container.
	for k := range int64(12) {
		m := minute(base+60*k, SourceBytes{"host", 10, 1}, SourceBytes{"container:x", 99, 99})
		if err := s.AddHistory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		res, from, to int64
		ok            bool
		rx            uint64
	}{
		{ResMinute, base, base + 600, true, 100},          // all 10 minutes
		{ResMinute, base, base + 900, true, 120},          // 12 of 15: 80%
		{ResMinute, base, base + 960, false, 0},           // 12 of 16: 75%
		{ResMinute, base - 3000, base + 600, false, 0},    // 10 of 60
		{ResHour, base, base + 3600, false, 0},            // the hour holds 12 of its 60 minutes
		{ResMinute, base + 720, base + 1200, false, 0},    // nothing kept
		{ResMinute, base + 600, base + 600, false, 0},     // an empty period
		{ResMinute, base + 600, base + 720, true, 20},     // the last two minutes
		{ResMinute, base + 660, base + 780, false, 0},     // one of two
		{ResMinute, base + 60, base + 60 + 300, true, 50}, // five in the middle
	} {
		rx, tx, ok, err := s.HostTraffic(ctx, c.res, c.from, c.to)
		if err != nil || ok != c.ok || rx != c.rx || (ok && tx != c.rx/10) {
			t.Errorf("res %d [%+d, %+d) = %d/%d %v %v, want %d %v", c.res, c.from-base, c.to-base, rx, tx, ok, err,
				c.rx, c.ok)
		}
	}
}

func TestAddHistoryKeepsEachMinuteOnce(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC).Unix()
	m := minute(base, SourceBytes{"host", 100, 10}, SourceBytes{"container:a", 7, 7})
	m.Host = &HostAvg{CPU: 50, Mem: 1000}
	again := minute(base, SourceBytes{"host", 900, 90}, SourceBytes{"container:a", 70, 70},
		SourceBytes{"container:b", 1, 1})
	again.Host = &HostAvg{CPU: 10, Mem: 10}
	for _, x := range []HistoryMinute{m, again, m} {
		if err := s.AddHistory(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	for _, res := range []int64{ResMinute, ResHour, ResDay} {
		ts := base - base%res
		rows, _ := s.TrafficSeries(ctx, res, ts, ts+1)
		host, _ := s.HostSeries(ctx, res, ts, ts+1)
		if len(rows) != 2 || rows[0] != (SeriesRow{ts, "container:a", 7, 7, 1}) || rows[1] != (SeriesRow{ts, "host", 100, 10, 1}) ||
			len(host) != 1 || host[0] != (HostRow{ts, 50, 1000, 1}) {
			t.Errorf("res %d after writing the minute three times = %v %v", res, rows, host)
		}
	}
}

func TestOtherIsOneRowPerBucket(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC).Unix()
	// Two minutes of the same hour, each with 70 containers, already capped as the sampler hands them over.
	for k := range int64(2) {
		rows := []SourceBytes{{"host", 6000, 600}}
		for i := range 70 {
			rows = append(rows, SourceBytes{fmt.Sprintf("container:c%02d", i), 10, 1})
		}
		if err := s.AddHistory(ctx, minute(base+60*k, CapSources(rows)...)); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		res, ts int64
		n       int64
	}{{ResMinute, base, 1}, {ResMinute, base + 60, 1}, {ResHour, base, 2}, {ResDay, base - base%86400, 2}} {
		rows, _ := s.TrafficSeries(ctx, c.res, c.ts, c.ts+1)
		others := 0
		var other, host SeriesRow
		for _, r := range rows {
			switch r.Source {
			case OtherSource:
				others++
				other = r
			case "host":
				host = r
			}
		}
		if len(rows) != MaxSources || others != 1 || other.N != c.n || other.Rx != uint64(c.n)*8*10 ||
			host.N != c.n || host.Rx != uint64(c.n)*6000 {
			t.Errorf("res %d at %d: %d rows, %d other rows, other %+v, host %+v", c.res, c.ts-base, len(rows), others,
				other, host)
		}
	}
}

func TestTrafficDays(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.AddTraffic(ctx, []TrafficRow{{"2026-11-01", "host", 1, 2}, {"2026-11-02", "host", 3, 4},
		{"2026-11-02", "container:a", 9, 9}, {"2026-11-03", "host", 5, 6}}); err != nil {
		t.Fatal(err)
	}
	days, err := s.TrafficDays(ctx, "host", "2026-11-02", "2026-11-03")
	if err != nil || len(days) != 1 || days["2026-11-02"] != (TrafficRow{"2026-11-02", "host", 3, 4}) {
		t.Fatalf("days = %v %v", days, err)
	}
}
