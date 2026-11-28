package system

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"observarr/internal/store"
)

func TestClockStepBackCountsMinutesOnce(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	tr := NewTraffic(zone, "", 0)
	tr.keep = true
	start := at("2026-11-23", "10:00:00")
	rx := uint64(0)
	read := func(m int) {
		tr.observe(start.Add(time.Duration(m)*time.Minute), eth(rx, rx/10), nil)
		tr.addHost(10, 100)
		rx += 60
	}
	for m := 0; m <= 10; m++ {
		read(m)
	}
	for m := 7; m <= 15; m++ { // the clock steps back three minutes
		read(m)
	}
	if err := tr.persistHistory(ctx, st); err != nil {
		t.Fatal(err)
	}

	hour := start.Unix()
	minutes, _ := st.TrafficSeries(ctx, store.ResMinute, hour, hour+3600)
	var sum uint64
	for _, m := range minutes {
		if m.N != 1 || m.Rx > 60 {
			t.Fatalf("minute %v: counted twice", m)
		}
		sum += m.Rx
	}
	if len(minutes) != 15 {
		t.Fatalf("%d minutes kept, want 15 (0 to 14)", len(minutes))
	}
	for _, res := range []int64{store.ResHour, store.ResDay} {
		ts := hour
		if res == store.ResDay {
			ts = dayStart(hour, zone)
		}
		rows, _ := st.TrafficSeries(ctx, res, ts, ts+1)
		host, _ := st.HostSeries(ctx, res, ts, ts+1)
		if len(rows) != 1 || rows[0].N != 15 || rows[0].Rx != sum || len(host) != 1 || host[0].N != 15 {
			t.Fatalf("res %d = %+v %+v, want 15 minutes and %d bytes", res, rows, host, sum)
		}
	}
}

// containers returns n containers' counters, the i-th at i*per bytes down per minute after m minutes.
func containers(n, m int, per uint64) []netCounters {
	out := make([]netCounters, n)
	for i := range out {
		out[i] = netCounters{name: fmt.Sprintf("c%02d", i), pid: 1, rx: uint64(m) * uint64(i) * per}
	}
	return out
}

func TestOtherDoesNotLowerTheRates(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	tr := NewTraffic(zone, "", 0)
	tr.keep = true
	start := at("2026-11-23", "10:00:00")
	// 70 containers, the i-th sending i bytes a second; the host 100 B/s. Two minutes closed.
	for m := 0; m <= 2; m++ {
		tr.observe(start.Add(time.Duration(m)*time.Minute), eth(uint64(m)*6000, 0), containers(70, m, 60))
	}
	if err := tr.persistHistory(ctx, st); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.TrafficSeries(ctx, store.ResHour, start.Unix(), start.Unix()+1)
	var other store.SeriesRow
	for _, r := range rows {
		if r.Source == store.OtherSource {
			other = r
		}
	}
	if len(rows) != store.MaxSources || other.N != 2 {
		t.Fatalf("hour: %d rows, other %+v; want one other row of 2 minutes", len(rows), other)
	}
	v, err := tr.longView(ctx, st, "7d", start.Add(30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	n := len(v.Times)
	if v.Host.Series.Rx[n-1] != 100 || len(v.Containers) != 62 || v.Containers[0].Name != "c69" ||
		v.Containers[0].Series.Rx[n-1] != 69 {
		t.Fatalf("7d: host %v B/s, %d containers, first %s at %v B/s", v.Host.Series.Rx[n-1], len(v.Containers),
			v.Containers[0].Name, v.Containers[0].Series.Rx[n-1])
	}
}

func TestDayViewShowsTheBusiestContainers(t *testing.T) {
	tr := NewTraffic(zone, "", 0)
	start := at("2026-11-23", "10:00:00")
	for m := 0; m <= 2; m++ {
		tr.observe(start.Add(time.Duration(m)*time.Minute), eth(0, 0), containers(maxSeries+6, m, 60))
	}
	v := tr.View("24h", start.Add(2*time.Minute))
	if len(v.Containers) != maxSeries {
		t.Fatalf("%d containers, want %d", len(v.Containers), maxSeries)
	}
	for _, c := range v.Containers {
		if c.Name < "c06" {
			t.Fatalf("%s, among the least busy, is shown", c.Name)
		}
	}
	if v.Containers[0].Name != fmt.Sprintf("c%02d", maxSeries+5) || v.Containers[0].Series.Rx[minuteSlots-1] != maxSeries+5 {
		t.Fatalf("first = %s %v", v.Containers[0].Name, v.Containers[0].Series.Rx[minuteSlots-1])
	}
}

// badMinute is a HistoryStore that refuses one minute.
type badMinute struct {
	*store.Store
	bad int64
}

func (b *badMinute) AddHistory(ctx context.Context, m store.HistoryMinute) error {
	if m.Minute == b.bad {
		return errors.New("constraint failed")
	}
	return b.Store.AddHistory(ctx, m)
}

func TestOneBadMinuteIsDropped(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	tr := NewTraffic(zone, "", 0)
	tr.keep = true
	start := at("2026-11-23", "10:00:00")
	for m := 0; m <= 3; m++ {
		tr.observe(start.Add(time.Duration(m)*time.Minute), eth(0, 0), nil)
	}
	b := &badMinute{Store: st, bad: start.Unix()}
	for i := 1; i < maxHeadFails; i++ {
		if err := tr.persistHistory(ctx, b); err == nil || strings.Contains(err.Error(), "dropped") {
			t.Fatalf("write %d = %v", i, err)
		}
	}
	if err := tr.persistHistory(ctx, b); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("write %d = %v, want the minute dropped", maxHeadFails, err)
	}
	if err := tr.persistHistory(ctx, b); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.TrafficSeries(ctx, store.ResMinute, 0, start.Unix()+3600); len(rows) != 2 {
		t.Fatalf("%d minutes written, want the 2 after the bad one", len(rows))
	}
}

func TestShutdownWritesTheOpenMinute(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	tr := NewTraffic(zone, "", 0)
	tr.keep = true
	start := at("2026-11-23", "10:00:00")
	tr.observe(start, eth(0, 0), nil)
	tr.observe(start.Add(30*time.Second), eth(3000, 300), nil)
	tr.flushOpen()
	tr.flushOpen() // once only
	if err := tr.persistHistory(ctx, st); err != nil {
		t.Fatal(err)
	}
	// Were it not stopped after all, the minute is not handed over again when it closes.
	tr.observe(start.Add(70*time.Second), eth(4000, 400), nil)
	if err := tr.persistHistory(ctx, st); err != nil {
		t.Fatal(err)
	}
	rows, _ := st.TrafficSeries(ctx, store.ResMinute, start.Unix(), start.Unix()+1)
	hour, _ := st.TrafficSeries(ctx, store.ResHour, start.Unix(), start.Unix()+1)
	if len(rows) != 1 || rows[0].Rx != 3000 || rows[0].Tx != 300 || len(hour) != 1 || hour[0].N != 1 {
		t.Fatalf("minute = %+v, hour = %+v; want the 3000/300 counted before stopping, once", rows, hour)
	}
}
