package system

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"observarr/internal/store"
)

// HistoryStore keeps traffic and host figures in minute, hour and day buckets (*store.Store).
type HistoryStore interface {
	AddHistory(ctx context.Context, m store.HistoryMinute) error
	TrafficSeries(ctx context.Context, res, from, to int64) ([]store.SeriesRow, error)
	HostSeries(ctx context.Context, res, from, to int64) ([]store.HostRow, error)
	HostTraffic(ctx context.Context, res, from, to int64) (rx, tx uint64, ok bool, err error)
	TrafficDays(ctx context.Context, scope, from, to string) (map[string]store.TrafficRow, error)
}

const (
	sparkMinutes = 60               // the minutes of a Spark
	dailyDays    = 30               // the days of TrafficView.Daily
	maxSeries    = store.MaxSources // containers a view shows at most
	maxHeadFails = 5                // failed writes of one minute before it is dropped
	minCoverage  = 80               // percent of a period's minutes that must be known for its total
)

// span is a range of buckets: their resolution in seconds and how many there are, the latest last.
type span struct {
	res    int64
	points int
}

// spans are the ranges besides "5m", the latest readings.
var spans = map[string]span{
	"24h": {store.ResMinute, minuteSlots},
	"7d":  {store.ResHour, 7 * 24},
	"30d": {store.ResHour, 30 * 24},
	"1y":  {store.ResDay, 366},
}

// ValidRange reports whether rng is a range Network and HostHistory take: "5m", "24h", "7d", "30d" or "1y".
func ValidRange(rng string) bool {
	_, ok := spans[rng]
	return ok || rng == "5m"
}

// Values are series values; NaN is a gap, encoded as null.
type Values []float64

// MarshalJSON writes the values as a JSON array, null for a gap.
func (vs Values) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 2+8*len(vs))
	b = append(b, '[')
	for i, v := range vs {
		if i > 0 {
			b = append(b, ',')
		}
		if math.IsNaN(v) || math.IsInf(v, 0) {
			b = append(b, "null"...)
		} else {
			b = strconv.AppendFloat(b, v, 'f', -1, 64)
		}
	}
	return append(b, ']'), nil
}

// gaps returns n values, all gaps.
func gaps(n int) Values {
	vs := make(Values, n)
	for i := range vs {
		vs[i] = math.NaN()
	}
	return vs
}

// hourStart is the start of the local hour ts (seconds since the epoch) is in. It is counted back from ts, so
// the hour repeated when clocks go back is two hours, never one.
func hourStart(ts int64, loc *time.Location) int64 {
	t := time.Unix(ts, 0).In(loc)
	return ts - int64(t.Minute()*60+t.Second())
}

// dayStart is the start of the local day ts is in: its midnight.
func dayStart(ts int64, loc *time.Location) int64 { return addDays(ts, 0, loc) }

// addDays is the start of the local day n days after the one ts is in (before it, for a negative n).
func addDays(ts int64, n int, loc *time.Location) int64 {
	y, m, d := time.Unix(ts, 0).In(loc).Date()
	return time.Date(y, m, d+n, 0, 0, 0, 0, loc).Unix()
}

// enqueue queues a closed minute for the history, the host first and the containers by most traffic, at most
// MaxSources of them. Beyond maxQueued minutes the oldest is dropped.
func (t *Traffic) enqueue(m store.HistoryMinute) {
	slices.SortFunc(m.Traffic, func(a, b store.SourceBytes) int {
		if (a.Source == hostScope) != (b.Source == hostScope) {
			if a.Source == hostScope {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(b.Rx+b.Tx, a.Rx+a.Tx), cmp.Compare(a.Source, b.Source))
	})
	m.Traffic = store.CapSources(m.Traffic)
	t.queue = append(t.queue, m)
	if over := len(t.queue) - maxQueued; over > 0 {
		t.queue = slices.Delete(t.queue, 0, over)
	}
}

// persistHistory writes the queued minutes to hs, each in a transaction of its own. Minutes that fail to be
// written wait for the next call, as long as the queue has room for them; a minute that failed maxHeadFails
// times in a row is dropped, so one bad minute cannot hold up the others.
func (t *Traffic) persistHistory(ctx context.Context, hs HistoryStore) error {
	t.mu.Lock()
	queue := t.queue
	t.queue = nil
	t.mu.Unlock()
	for i, m := range queue {
		err := hs.AddHistory(ctx, m)
		t.mu.Lock()
		if err == nil {
			t.headFails = 0
			t.mu.Unlock()
			continue
		}
		rest := queue[i:]
		if t.headFails++; t.headFails >= maxHeadFails {
			rest, t.headFails = rest[1:], 0
			err = fmt.Errorf("dropped the minute at %s after %d failed writes: %w",
				time.Unix(m.Minute, 0).UTC().Format(time.RFC3339), maxHeadFails, err)
		}
		t.queue = append(rest, t.queue...)
		if over := len(t.queue) - maxQueued; over > 0 {
			t.queue = slices.Delete(t.queue, 0, over)
		}
		t.mu.Unlock()
		return err
	}
	return nil
}

// restore reads the last day's minutes back from hs, so the day's series holds what was counted before a
// restart. Call it before the first reading.
func (t *Traffic) restore(ctx context.Context, hs HistoryStore, now time.Time) error {
	cur := minuteOf(now)
	from, to := (cur-minuteSlots)*60, cur*60
	rows, err := hs.TrafficSeries(ctx, store.ResMinute, from, to)
	if err != nil {
		return err
	}
	hosts, err := hs.HostSeries(ctx, store.ResMinute, from, to)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	hold := func(m int64) int {
		i := slot(m)
		if t.minuteAt[i] != m {
			t.minuteAt[i] = m
			for _, f := range t.flows {
				f.minutes[i] = pair[float32]{}
			}
		}
		t.lastQueued = max(t.lastQueued, m) // kept already: never handed to the history again
		return i
	}
	for _, r := range rows {
		i := hold(r.TS / 60)
		f := t.track(r.Source, time.Unix(r.TS, 0))
		if f == nil {
			continue
		}
		if at := time.Unix(r.TS+60, 0); at.After(f.seen) {
			f.seen = at
		}
		f.minutes[i] = pair[float32]{float32(r.Rx) / 60, float32(r.Tx) / 60}
	}
	for _, h := range hosts {
		m := h.TS / 60
		i := hold(m)
		t.hostAt[i], t.cpu[i], t.mem[i] = m, float32(h.CPU), h.Mem
	}
	return nil
}

// grid returns the starts (seconds since the epoch) of sp's buckets up to the one now is in, oldest first.
// Minute buckets end with the latest closed minute. Call it with t.mu held.
func (t *Traffic) grid(sp span, now time.Time) []int64 {
	out := make([]int64, sp.points)
	switch sp.res {
	case store.ResMinute:
		last := t.lastClosed(now)
		for i := range out {
			out[i] = (last - int64(sp.points-1-i)) * 60
		}
	case store.ResHour:
		last := hourStart(now.Unix(), t.loc)
		for i := range out {
			out[i] = last - int64(sp.points-1-i)*3600
		}
	default:
		for i := range out {
			out[i] = addDays(now.Unix(), i-(sp.points-1), t.loc)
		}
	}
	return out
}

// liveRates are f's rates in the latest reading. Call it with t.mu held.
func (t *Traffic) liveRates(f *flow) (rx, tx float64) {
	latest := (t.nextLive + liveSlots - 1) % liveSlots
	if f == nil || t.liveAt[latest] != t.last.UnixMilli() {
		return 0, 0
	}
	return float64(f.live[latest][0]), float64(f.live[latest][1])
}

// longView returns the traffic over rng ("7d", "30d" or "1y") from hs: hour or day buckets, each value the
// average rate over the minutes of the bucket that were watched, a gap for a bucket nobody watched. It
// shows the MaxSources containers with the most traffic in the range.
func (t *Traffic) longView(ctx context.Context, hs HistoryStore, rng string, now time.Time) (TrafficView, error) {
	sp := spans[rng]
	t.mu.Lock()
	starts := t.grid(sp, now)
	t.mu.Unlock()
	idx := make(map[int64]int, len(starts))
	for i, s := range starts {
		idx[s] = i
	}
	var rows []store.SeriesRow
	if hs != nil {
		var err error
		if rows, err = hs.TrafficSeries(ctx, sp.res, starts[0], starts[len(starts)-1]+1); err != nil {
			return TrafficView{}, err
		}
	}
	minutes := make([]int64, len(starts)) // watched minutes per bucket: the host row's, written every one
	bytes := map[string][]pair[uint64]{}
	totals := map[string]uint64{}
	for _, r := range rows {
		i, ok := idx[r.TS]
		if !ok {
			continue
		}
		if r.Source == hostScope {
			minutes[i] = r.N
		}
		if r.Source != hostScope && !strings.HasPrefix(r.Source, ctrScope) {
			continue // the containers beyond a bucket's sources: the host's figures cover them
		}
		b := bytes[r.Source]
		if b == nil {
			b = make([]pair[uint64], len(starts))
			bytes[r.Source] = b
		}
		b[i] = pair[uint64]{b[i][0] + r.Rx, b[i][1] + r.Tx}
		totals[r.Source] += r.Rx + r.Tx
	}
	series := func(b []pair[uint64]) *Series {
		s := &Series{Rx: gaps(len(starts)), Tx: gaps(len(starts))}
		for i, n := range minutes {
			if n == 0 {
				continue
			}
			s.Rx[i], s.Tx[i] = 0, 0
			if b != nil {
				secs := float64(n * 60)
				s.Rx[i], s.Tx[i] = math.Round(float64(b[i][0])/secs), math.Round(float64(b[i][1])/secs)
			}
		}
		if !s.moves() {
			return nil
		}
		return s
	}
	var names []string
	for src := range bytes {
		if strings.HasPrefix(src, ctrScope) {
			names = append(names, src)
		}
	}
	slices.SortFunc(names, func(a, b string) int { return cmp.Or(cmp.Compare(totals[b], totals[a]), cmp.Compare(a, b)) })
	names = names[:min(len(names), maxSeries)]

	t.mu.Lock()
	defer t.mu.Unlock()
	v := TrafficView{TrafficSummary: t.summary(), Range: rng, Step: sp.res, Times: make([]int64, len(starts)),
		Containers: []ContainerTraffic{}}
	for i, s := range starts {
		v.Times[i] = s * 1000
	}
	v.Host.Rx, v.Host.Tx = t.liveRates(t.flows[hostScope])
	v.Host.Series = series(bytes[hostScope])
	for _, src := range names {
		c := ContainerTraffic{Name: strings.TrimPrefix(src, ctrScope)}
		if f := t.flows[src]; f != nil {
			c.Rx, c.Tx = t.liveRates(f)
			c.TodayRx, c.TodayTx = f.today[0]+uint64(f.open[0]), f.today[1]+uint64(f.open[1])
		}
		c.Series = series(bytes[src])
		v.Containers = append(v.Containers, c) // most traffic in the range first
	}
	return v, nil
}

// previous returns the host's bytes in the period as long as rng right before the one v shows, or nil when
// that period is not known in full. "5m" comes from memory, the other ranges from hs; "30d" and "1y" from
// local days, since hours are not kept that long.
func (t *Traffic) previous(ctx context.Context, hs HistoryStore, rng string, v TrafficView) (*Totals, error) {
	if rng == "5m" {
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.open == 0 {
			return nil, nil
		}
		var sum pair[float64]
		held := 0
		host := t.flows[hostScope]
		for m := t.open - 10; m < t.open-5; m++ {
			if i := slot(m); t.minuteAt[i] == m {
				held++
				sum[0] += float64(host.minutes[i][0]) * 60
				sum[1] += float64(host.minutes[i][1]) * 60
			}
		}
		if held*100 < 5*minCoverage {
			return nil, nil
		}
		return &Totals{In: uint64(math.Round(sum[0])), Out: uint64(math.Round(sum[1]))}, nil
	}
	if hs == nil || len(v.Times) == 0 {
		return nil, nil
	}
	sp := spans[rng]
	first := v.Times[0] / 1000
	res, from := sp.res, first-int64(sp.points)*sp.res
	switch rng {
	case "30d":
		res, first = store.ResDay, dayStart(first, t.loc)
		from = addDays(first, -30, t.loc)
	case "1y":
		from = addDays(first, -sp.points, t.loc)
	}
	rx, tx, ok, err := hs.HostTraffic(ctx, res, from, first)
	if err != nil || !ok {
		return nil, err
	}
	return &Totals{In: rx, Out: tx}, nil
}

// daily returns the host's bytes on each of the last dailyDays local days, oldest first: today's as counted,
// the others' from hs.
func (t *Traffic) daily(ctx context.Context, hs HistoryStore, now time.Time) ([]DayTotal, error) {
	t.mu.Lock()
	sum := t.summary()
	today := t.day
	t.mu.Unlock()
	if today == "" {
		today = now.In(t.loc).Format(time.DateOnly)
	}
	end, err := time.ParseInLocation(time.DateOnly, today, t.loc)
	if err != nil {
		return nil, err
	}
	out := make([]DayTotal, dailyDays)
	for i := range out {
		out[i].Day = end.AddDate(0, 0, i-(dailyDays-1)).Format(time.DateOnly)
	}
	out[dailyDays-1].In, out[dailyDays-1].Out = &sum.TodayRx, &sum.TodayTx
	if hs == nil {
		return out, nil
	}
	days, err := hs.TrafficDays(ctx, hostScope, out[0].Day, today)
	if err != nil {
		return nil, err
	}
	for i := range out[:dailyDays-1] {
		if r, ok := days[out[i].Day]; ok {
			out[i].In, out[i].Out = &r.Rx, &r.Tx
		}
	}
	return out, nil
}

// HostSeries is the host's CPU use (percent of all CPUs) and memory used (bytes) over a range, as GET
// /api/system/history?range= returns it: one value per time, a gap where nothing was read.
type HostSeries struct {
	Range   string  `json:"range"`
	Step    int64   `json:"step"`  // seconds between times: nominal for "5m", whose times are the readings
	Times   []int64 `json:"times"` // ms since epoch
	CPU     Values  `json:"cpu"`
	MemUsed Values  `json:"memUsed"`
}

// Spark is the host's last hour of CPU use and memory used, by minute, with the averages of the hour before
// it (nil when nothing was read then).
type Spark struct {
	Spark       HostSeries `json:"spark"`
	PreviousCPU *float64   `json:"previousCpu"`
	PreviousMem *float64   `json:"previousMem"`
}

// round2 rounds a percentage to two decimals.
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// hostMinutes returns the host's averages in the n minutes up to last from memory. Call it with t.mu held.
func (t *Traffic) hostMinutes(rng string, last int64, n int) HostSeries {
	hs := HostSeries{Range: rng, Step: 60, Times: make([]int64, n), CPU: gaps(n), MemUsed: gaps(n)}
	for k := range n {
		m := last - int64(n-1-k)
		hs.Times[k] = m * 60_000
		if i := slot(m); t.hostAt[i] == m {
			hs.CPU[k], hs.MemUsed[k] = round2(float64(t.cpu[i])), float64(t.mem[i])
		}
	}
	return hs
}

// spark returns the last sparkMinutes closed minutes and the averages of those before them.
func (t *Traffic) spark(now time.Time) Spark {
	t.mu.Lock()
	defer t.mu.Unlock()
	last := t.lastClosed(now)
	s := Spark{Spark: t.hostMinutes("1h", last, sparkMinutes)}
	var cpu, mem float64
	n := 0
	for m := last - 2*sparkMinutes + 1; m <= last-sparkMinutes; m++ {
		if i := slot(m); t.hostAt[i] == m {
			cpu += float64(t.cpu[i])
			mem += float64(t.mem[i])
			n++
		}
	}
	if n > 0 {
		c, m := round2(cpu/float64(n)), math.Round(mem/float64(n))
		s.PreviousCPU, s.PreviousMem = &c, &m
	}
	return s
}

// hostHistory returns the host's averages over rng ("24h" from memory, "7d", "30d" or "1y" from hs).
func (t *Traffic) hostHistory(ctx context.Context, hs HistoryStore, rng string, now time.Time) (HostSeries, error) {
	sp := spans[rng]
	t.mu.Lock()
	if sp.res == store.ResMinute {
		defer t.mu.Unlock()
		return t.hostMinutes(rng, t.lastClosed(now), sp.points), nil
	}
	starts := t.grid(sp, now)
	t.mu.Unlock()
	out := HostSeries{Range: rng, Step: sp.res, Times: make([]int64, len(starts)), CPU: gaps(len(starts)),
		MemUsed: gaps(len(starts))}
	idx := make(map[int64]int, len(starts))
	for i, s := range starts {
		idx[s] = i
		out.Times[i] = s * 1000
	}
	if hs == nil {
		return out, nil
	}
	rows, err := hs.HostSeries(ctx, sp.res, starts[0], starts[len(starts)-1]+1)
	if err != nil {
		return HostSeries{}, err
	}
	for _, r := range rows {
		if i, ok := idx[r.TS]; ok {
			out.CPU[i], out.MemUsed[i] = round2(r.CPU), float64(r.Mem)
		}
	}
	return out, nil
}
