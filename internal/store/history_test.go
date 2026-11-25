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
		36 * 24 * time.Hour, 399 * 24 * time.Hour, 401 * 24 * time.Hour} {
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

func TestHostTrafficNeedsTheWholePeriod(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	base := int64(1_800_000_000)
	if _, _, ok, err := s.HostTraffic(ctx, ResMinute, base, base+600); ok || err != nil {
		t.Fatalf("an empty history = %v %v", ok, err)
	}
	for _, m := range []HistoryMinute{
		minute(base+300, SourceBytes{"host", 10, 1}, SourceBytes{"container:x", 99, 99}),
		minute(base+360, SourceBytes{"host", 20, 2}),
		minute(base+900, SourceBytes{"host", 40, 4}),
	} {
		if err := s.AddHistory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// The history begins five minutes into [base, base+600): it does not cover that period.
	if _, _, ok, _ := s.HostTraffic(ctx, ResMinute, base, base+600); ok {
		t.Fatal("a period only partly kept was reported")
	}
	rx, tx, ok, err := s.HostTraffic(ctx, ResMinute, base+300, base+900)
	if err != nil || !ok || rx != 30 || tx != 3 {
		t.Fatalf("host traffic = %d/%d %v %v, want 30/3 from the host's rows only", rx, tx, ok, err)
	}
	if _, _, ok, _ := s.HostTraffic(ctx, ResMinute, base+420, base+900); ok {
		t.Fatal("a period without rows was reported")
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
