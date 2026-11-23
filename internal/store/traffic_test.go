package store

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestTrafficAddsSumsAndPrunes(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	add := func(rows ...TrafficRow) {
		t.Helper()
		if err := s.AddTraffic(ctx, rows); err != nil {
			t.Fatal(err)
		}
	}
	add(TrafficRow{"2026-10-31", "host", 1, 2}, TrafficRow{"2026-11-01", "host", 10, 20},
		TrafficRow{"2026-11-01", "container:app", 3, 4})
	add(TrafficRow{"2026-11-01", "host", 5, 5}, TrafficRow{"2026-11-02", "host", 100, 200})
	add()

	day, err := s.TrafficDay(ctx, "2026-11-01")
	slices.SortFunc(day, func(a, b TrafficRow) int { return len(a.Scope) - len(b.Scope) })
	want := []TrafficRow{{"2026-11-01", "host", 15, 25}, {"2026-11-01", "container:app", 3, 4}}
	if err != nil || !slices.Equal(day, want) {
		t.Fatalf("day = %v %v, want %v", day, err, want)
	}
	// November up to (not including) the 2nd: only the 1st, and only the host.
	if n, err := s.TrafficSum(ctx, "host", "2026-11-01", "2026-11-02"); err != nil || n != 40 {
		t.Fatalf("sum = %d %v, want 40", n, err)
	}
	if n, err := s.TrafficSum(ctx, "host", "2026-12-01", "2026-12-02"); err != nil || n != 0 {
		t.Fatalf("empty sum = %d %v", n, err)
	}

	// 35 days are kept: 2026-11-01 is 35 days before 2026-12-06, 2026-10-31 is one more.
	if err := s.PruneTraffic(ctx, time.Date(2026, 12, 6, 23, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.TrafficSum(ctx, "host", "2000-01-01", "2100-01-01"); n != 40+300 {
		t.Fatalf("after pruning = %d, want 340", n)
	}
}
