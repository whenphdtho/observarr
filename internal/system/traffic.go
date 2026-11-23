package system

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"observarr/internal/collect"
	"observarr/internal/store"
)

const (
	minuteSlots = 24 * 60        // a day of 1-minute points
	liveSlots   = maxHistory     // the latest readings, as many as the host history keeps
	maxTracked  = 200            // containers tracked at most; the traffic of others only counts for the host
	trackedFor  = 24 * time.Hour // a container not seen this long is no longer tracked
	maxGap      = time.Hour      // counters read further apart are not compared (suspended host, clock jump)
	maxPoints   = 300            // values per series at most in a TrafficView
	maxPending  = 10_000         // day totals kept at most while the database takes no writes
	hostScope   = "host"
	ctrScope    = "container:" // followed by the container's name
)

// TrafficStore keeps daily traffic totals (*store.Store).
type TrafficStore interface {
	AddTraffic(ctx context.Context, rows []store.TrafficRow) error
	TrafficDay(ctx context.Context, day string) ([]store.TrafficRow, error)
	TrafficSum(ctx context.Context, scope, from, to string) (uint64, error)
}

// pair is down (rx) and up (tx): bytes, or bytes per second.
type pair[T float32 | float64 | uint64] [2]T

// netCounters is a container's cumulative network counters; a new pid means new counters.
type netCounters struct {
	name   string
	pid    int
	rx, tx uint64
}

// flow is one scope's traffic: the host's physical interfaces together, or one container's own network.
type flow struct {
	minutes [minuteSlots]pair[float32] // average rates, aligned with Traffic.minuteAt
	live    [liveSlots]pair[float32]   // rates, aligned with Traffic.liveAt
	open    pair[float64]              // bytes in the open minute
	today   pair[uint64]               // bytes in today's closed minutes

	prev  map[string]pair[uint64] // counters by interface ("" for a container)
	pid   int
	at    time.Time // when prev was read; zero before the first reading
	seen  time.Time
	delta pair[float64] // bytes from `from` to the latest reading, until spread over minutes
	from  time.Time
	rate  pair[float32] // over that interval
}

// Traffic keeps the network traffic of the host and of each container with a network of its own: the latest
// readings (five minutes at 2 s), a day of 1-minute averages, today's totals and peak, and the daily totals it
// hands to a TrafficStore. Its memory is bounded: fixed-size rings for at most maxTracked containers.
type Traffic struct {
	mu       sync.Mutex
	loc      *time.Location // days begin at midnight here
	period   string         // budget period, "day" or "month"; "" = no budget
	limit    uint64
	flows    map[string]*flow // by scope; always holds the host
	minuteAt [minuteSlots]int64
	nextMin  int
	liveAt   [liveSlots]int64 // ms since epoch
	nextLive int
	last     time.Time                  // latest reading
	open     int64                      // the minute (since epoch) being collected; 0 before the first reading
	day      string                     // the local day of open
	dayStart int64                      // the first minute of day
	month    uint64                     // host bytes of the month's days before day
	pending  map[[2]string]pair[uint64] // bytes by day and scope, not yet written
}

// TrafficSummary is the host's traffic today in bytes, its highest 1-minute rates today in bytes per second,
// and the budget.
type TrafficSummary struct {
	TodayRx uint64  `json:"todayRx"`
	TodayTx uint64  `json:"todayTx"`
	PeakRx  float64 `json:"peakRx"`
	PeakTx  float64 `json:"peakTx"`
	Budget  *Budget `json:"budget"` // nil without a budget
}

// Budget is a traffic budget and what of it is used, in bytes down and up together, this day or month.
type Budget struct {
	Period string `json:"period"`
	Limit  uint64 `json:"limitBytes"`
	Used   uint64 `json:"usedBytes"`
}

// TrafficView is the traffic over a range, as GET /api/network returns it. Rates are bytes per second.
type TrafficView struct {
	TrafficSummary
	Range      string             `json:"range"`
	Times      []int64            `json:"times"` // ms since epoch, one per series value
	Host       FlowView           `json:"host"`
	Containers []ContainerTraffic `json:"containers"` // most traffic today first
}

// FlowView is one scope's latest rates and its series over the range.
type FlowView struct {
	Rx     float64 `json:"rx"`
	Tx     float64 `json:"tx"`
	Series *Series `json:"series"` // nil when there was no traffic in the range
}

// Series holds one rate per TrafficView.Times.
type Series struct {
	Rx []float64 `json:"rx"`
	Tx []float64 `json:"tx"`
}

// ContainerTraffic is one container's traffic. Service names the service linked to it, if any.
type ContainerTraffic struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	FlowView
	TodayRx uint64 `json:"todayRx"`
	TodayTx uint64 `json:"todayTx"`
}

// NewTraffic returns an empty Traffic whose days begin at midnight in loc, with a budget of limit bytes per
// period ("day" or "month"); limit 0 means no budget.
func NewTraffic(loc *time.Location, period string, limit uint64) *Traffic {
	if limit == 0 {
		period = ""
	}
	return &Traffic{loc: loc, period: period, limit: limit, flows: map[string]*flow{hostScope: {}},
		pending: map[[2]string]pair[uint64]{}}
}

func minuteOf(t time.Time) int64 { return t.Unix() / 60 }

// load adds today's totals, and the host's bytes of this month's earlier days, from st.
func (t *Traffic) load(ctx context.Context, st TrafficStore, now time.Time) error {
	day := now.In(t.loc).Format(time.DateOnly)
	rows, err := st.TrafficDay(ctx, day)
	if err != nil {
		return err
	}
	month, err := st.TrafficSum(ctx, hostScope, day[:len("2006-01-")]+"01", day)
	if err != nil {
		return err
	}
	slices.SortFunc(rows, func(a, b store.TrafficRow) int { return cmp.Compare(b.Rx+b.Tx, a.Rx+a.Tx) })
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.day != day {
		t.startDay(day)
	}
	t.month = month
	for _, r := range rows {
		f := t.flows[r.Scope]
		if f == nil {
			if !strings.HasPrefix(r.Scope, ctrScope) || len(t.flows) > maxTracked {
				continue
			}
			f = &flow{seen: now}
			t.flows[r.Scope] = f
		}
		f.today = pair[uint64]{f.today[0] + r.Rx, f.today[1] + r.Tx}
	}
	return nil
}

// startDay makes day the current one: today's totals start at zero, and the month's earlier days are counted
// on (or anew in another month).
func (t *Traffic) startDay(day string) {
	host := t.flows[hostScope]
	if t.day != "" && t.day[:7] == day[:7] {
		t.month += host.today[0] + host.today[1]
	} else {
		t.month = 0
	}
	for _, f := range t.flows {
		f.today = pair[uint64]{}
	}
	start, _ := time.ParseInLocation(time.DateOnly, day, t.loc)
	t.day, t.dayStart = day, minuteOf(start)
}

// openMinute makes m the minute being collected, starting a new day when m is in another one.
func (t *Traffic) openMinute(m int64) {
	t.open = m
	if day := time.Unix(m*60, 0).In(t.loc).Format(time.DateOnly); day != t.day {
		t.startDay(day)
	}
}

// read takes a scope's counters. Each counter's increase since the previous reading is the scope's traffic
// in between; a counter that went back (reset), a new pid (restarted container: new counters) or a gap
// longer than maxGap gives no traffic for that interval, never a negative one.
func (f *flow) read(now time.Time, pid int, cur map[string]pair[uint64]) {
	f.seen = now
	if !f.at.IsZero() && pid == f.pid && now.After(f.at) && now.Sub(f.at) <= maxGap {
		var d pair[uint64]
		for k, c := range cur {
			if p, ok := f.prev[k]; ok {
				for i := range c {
					if c[i] >= p[i] {
						d[i] += c[i] - p[i]
					}
				}
			}
		}
		dt := now.Sub(f.at).Seconds()
		f.delta, f.from = pair[float64]{float64(d[0]), float64(d[1])}, f.at
		f.rate = pair[float32]{float32(float64(d[0]) / dt), float32(float64(d[1]) / dt)}
	} else {
		f.rate = pair[float32]{}
	}
	f.prev, f.pid, f.at = cur, pid, now
}

// observe takes one reading of the host's interfaces (nil when the host could not be read) and of the
// running containers that have a network of their own.
func (t *Traffic) observe(now time.Time, ifs []collect.NetIf, ctrs []netCounters) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ifs != nil {
		cur := make(map[string]pair[uint64], len(ifs))
		for _, n := range ifs {
			cur[n.Name] = pair[uint64]{n.RxBytes, n.TxBytes}
		}
		t.flows[hostScope].read(now, 0, cur)
	}
	for _, c := range ctrs {
		f := t.flows[ctrScope+c.name]
		if f == nil {
			if len(t.flows) > maxTracked {
				continue
			}
			f = &flow{}
			t.flows[ctrScope+c.name] = f
		}
		f.read(now, c.pid, map[string]pair[uint64]{"": {c.rx, c.tx}})
	}

	// Spread the bytes read over the minutes they cover, closing each minute that ended.
	cur := minuteOf(now)
	if t.open == 0 || cur < t.open { // the first reading, or the clock went back
		t.openMinute(cur)
	}
	for t.open < cur {
		t.spread(now)
		t.closeMinute()
		t.openMinute(max(t.open+1, cur-minuteSlots)) // after a long gap only the last day's minutes matter
	}
	t.spread(now)

	ms := now.UnixMilli()
	t.liveAt[t.nextLive] = ms
	for key, f := range t.flows {
		f.delta = pair[float64]{}
		f.live[t.nextLive] = pair[float32]{}
		if f.at.Equal(now) {
			f.live[t.nextLive] = f.rate
		}
		if key != hostScope && now.Sub(f.seen) > trackedFor {
			delete(t.flows, key)
		}
	}
	t.nextLive = (t.nextLive + 1) % liveSlots
	t.last = now
}

// spread adds to each flow's open minute its share of the bytes read: the part of the reading's interval
// (up to now) that falls into the minute. Bytes from before the open minute (a reading that came late) count
// for it, since earlier minutes are closed.
func (t *Traffic) spread(now time.Time) {
	start, end := time.Unix(t.open*60, 0), time.Unix(t.open*60+60, 0)
	for _, f := range t.flows {
		if f.delta == (pair[float64]{}) {
			continue
		}
		from := f.from
		if from.Before(start) {
			from = start
		}
		to := now
		if end.Before(now) {
			to = end
		}
		total, part := now.Sub(from).Seconds(), to.Sub(from).Seconds()
		if total <= 0 || part <= 0 {
			continue
		}
		for i := range f.delta {
			f.open[i] += f.delta[i] * part / total
		}
		if end.Before(now) { // the rest belongs to later minutes
			f.delta = pair[float64]{f.delta[0] * (1 - part/total), f.delta[1] * (1 - part/total)}
			f.from = end
		} else {
			f.delta = pair[float64]{}
		}
	}
}

// closeMinute records the open minute's averages and adds its bytes to today's totals and to those waiting
// to be written.
func (t *Traffic) closeMinute() {
	i := t.nextMin
	t.minuteAt[i] = t.open
	for key, f := range t.flows {
		f.minutes[i] = pair[float32]{float32(f.open[0] / 60), float32(f.open[1] / 60)}
		b := pair[uint64]{uint64(math.Round(f.open[0])), uint64(math.Round(f.open[1]))}
		f.open = pair[float64]{}
		if b == (pair[uint64]{}) {
			continue
		}
		f.today = pair[uint64]{f.today[0] + b[0], f.today[1] + b[1]}
		k := [2]string{t.day, key}
		if p, ok := t.pending[k]; ok || len(t.pending) < maxPending {
			t.pending[k] = pair[uint64]{p[0] + b[0], p[1] + b[1]}
		}
	}
	t.nextMin = (i + 1) % minuteSlots
}

// persist writes the totals waiting to be written to st. Totals that fail to be written wait for the next
// call; only on shutdown (final) is the open minute's part counted too, as it would otherwise be lost.
func (t *Traffic) persist(ctx context.Context, st TrafficStore, final bool) error {
	t.mu.Lock()
	if final {
		for key, f := range t.flows {
			b := pair[uint64]{uint64(math.Round(f.open[0])), uint64(math.Round(f.open[1]))}
			if b != (pair[uint64]{}) {
				k := [2]string{t.day, key}
				p := t.pending[k]
				t.pending[k] = pair[uint64]{p[0] + b[0], p[1] + b[1]}
			}
		}
	}
	rows := make([]store.TrafficRow, 0, len(t.pending))
	for k, b := range t.pending {
		rows = append(rows, store.TrafficRow{Day: k[0], Scope: k[1], Rx: b[0], Tx: b[1]})
	}
	t.pending = map[[2]string]pair[uint64]{}
	t.mu.Unlock()

	err := st.AddTraffic(ctx, rows)
	if err != nil {
		t.mu.Lock()
		for _, r := range rows {
			k := [2]string{r.Day, r.Scope}
			if p, ok := t.pending[k]; ok || len(t.pending) < maxPending {
				t.pending[k] = pair[uint64]{p[0] + r.Rx, p[1] + r.Tx}
			}
		}
		t.mu.Unlock()
	}
	return err
}

// Summary returns the host's traffic today, its peak and the budget.
func (t *Traffic) Summary() TrafficSummary {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.summary()
}

func (t *Traffic) summary() TrafficSummary {
	host := t.flows[hostScope]
	rx, tx := host.today[0]+uint64(host.open[0]), host.today[1]+uint64(host.open[1])
	s := TrafficSummary{TodayRx: rx, TodayTx: tx}
	for i, m := range t.minuteAt {
		if m >= t.dayStart && m > 0 {
			s.PeakRx = max(s.PeakRx, float64(host.minutes[i][0]))
			s.PeakTx = max(s.PeakTx, float64(host.minutes[i][1]))
		}
	}
	if t.period != "" {
		s.Budget = &Budget{Period: t.period, Limit: t.limit, Used: rx + tx}
		if t.period == "month" {
			s.Budget.Used += t.month
		}
	}
	return s
}

// View returns the traffic over the last five minutes (rng "5m", every reading) or the last day (any other
// rng, 1-minute averages, downsampled to at most maxPoints values).
func (t *Traffic) View(rng string) TrafficView {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := TrafficView{TrafficSummary: t.summary(), Range: rng, Times: []int64{}, Containers: []ContainerTraffic{}}

	// The slots in the range, oldest first.
	var slots []int
	if rng == "5m" {
		cut := t.last.Add(-historyWindow).UnixMilli()
		for n := range liveSlots {
			if i := (t.nextLive + n) % liveSlots; t.liveAt[i] > cut {
				slots = append(slots, i)
				v.Times = append(v.Times, t.liveAt[i])
			}
		}
	} else {
		v.Range = "24h"
		cut := minuteOf(t.last) - minuteSlots
		for n := range minuteSlots {
			if i := (t.nextMin + n) % minuteSlots; t.minuteAt[i] > cut && t.minuteAt[i] > 0 {
				slots = append(slots, i)
			}
		}
	}
	group := max(1, (len(slots)+maxPoints-1)/maxPoints)
	if rng != "5m" {
		for g := 0; g < len(slots); g += group {
			v.Times = append(v.Times, t.minuteAt[slots[g]]*60_000)
		}
	}
	latest := (t.nextLive + liveSlots - 1) % liveSlots
	view := func(f *flow) FlowView {
		fv := FlowView{}
		if t.liveAt[latest] == t.last.UnixMilli() {
			fv.Rx, fv.Tx = float64(f.live[latest][0]), float64(f.live[latest][1])
		}
		ring := f.minutes[:]
		if rng == "5m" {
			ring = f.live[:]
		}
		s, nonzero := &Series{Rx: []float64{}, Tx: []float64{}}, false
		for g := 0; g < len(slots); g += group {
			var sum pair[float64]
			n := min(group, len(slots)-g)
			for _, i := range slots[g : g+n] {
				sum[0] += float64(ring[i][0])
				sum[1] += float64(ring[i][1])
			}
			rx, tx := math.Round(sum[0]/float64(n)), math.Round(sum[1]/float64(n))
			s.Rx, s.Tx = append(s.Rx, rx), append(s.Tx, tx)
			nonzero = nonzero || rx > 0 || tx > 0
		}
		if nonzero {
			fv.Series = s
		}
		return fv
	}
	v.Host = view(t.flows[hostScope])
	for key, f := range t.flows {
		if name, ok := strings.CutPrefix(key, ctrScope); ok {
			v.Containers = append(v.Containers, ContainerTraffic{Name: name, FlowView: view(f),
				TodayRx: f.today[0] + uint64(f.open[0]), TodayTx: f.today[1] + uint64(f.open[1])})
		}
	}
	slices.SortFunc(v.Containers, func(a, b ContainerTraffic) int {
		return cmp.Or(cmp.Compare(b.TodayRx+b.TodayTx, a.TodayRx+a.TodayTx), cmp.Compare(a.Name, b.Name))
	})
	return v
}
