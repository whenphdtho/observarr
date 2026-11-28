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
	maxPending  = 10_000         // day totals kept at most while the database takes no writes
	maxQueued   = 120            // minutes kept at most for the history while the database takes no writes
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
	minutes [minuteSlots]pair[float32] // average rates; the minute m at slot(m), see Traffic.minuteAt
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
// hands to a TrafficStore. It also keeps a day of the host's 1-minute CPU and memory averages, and hands each
// closed minute to a HistoryStore. Its memory is bounded: fixed-size rings for at most maxTracked containers,
// and at most maxQueued minutes waiting to be written.
type Traffic struct {
	mu     sync.Mutex
	loc    *time.Location // days begin at midnight here
	period string         // budget period, "day" or "month"; "" = no budget
	limit  uint64
	flows  map[string]*flow // by scope; always holds the host
	// The rings hold the minute m (since epoch) at slot(m) when minuteAt there is m. A minute nobody watched
	// (Observarr was not running, or the readings around it were too far apart to trust) is not held.
	minuteAt [minuteSlots]int64
	hostAt   [minuteSlots]int64 // likewise for cpu and mem, which only minutes with a host reading have
	cpu      [minuteSlots]float32
	mem      [minuteSlots]uint64
	liveAt   [liveSlots]int64 // ms since epoch
	nextLive int
	last     time.Time                  // latest reading
	open     int64                      // the minute (since epoch) being collected; 0 before the first reading
	day      string                     // the local day of open
	month    uint64                     // host bytes of the month's days before day
	peak     pair[float32]              // the host's highest rates in a reading of day (since the start, on the first day)
	pending  map[[2]string]pair[uint64] // bytes by day and scope, not yet written

	seen       bool    // a reading was taken in the open minute
	openCPU    float64 // sum of the open minute's host CPU readings
	openMem    float64 // and of its memory readings
	openHost   int     // how many host readings that is
	keep       bool    // closed minutes are queued for a HistoryStore
	lastQueued int64   // the latest minute queued (or restored); none at or before it is queued again
	queue      []store.HistoryMinute
	headFails  int // failed writes in a row of the queue's first minute
}

// TrafficSummary is the host's traffic today in bytes, its highest rates today in bytes per second (of one
// reading, every 2 s while someone watches), and the budget.
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
	Step       int64              `json:"step"`  // seconds between times: nominal for "5m", whose times are the readings
	Times      []int64            `json:"times"` // ms since epoch, one per series value
	Host       FlowView           `json:"host"`
	Containers []ContainerTraffic `json:"containers"` // most traffic today first
	Previous   *Totals            `json:"previous"`   // the host's bytes in the period as long before the range; nil = unknown
	Daily      []DayTotal         `json:"daily"`      // the host's bytes on the last 30 local days, today last
}

// Totals are bytes down (In) and up (Out).
type Totals struct {
	In  uint64 `json:"in"`
	Out uint64 `json:"out"`
}

// DayTotal is the host's bytes on one local day; nil when that day is not known.
type DayTotal struct {
	Day string  `json:"day"` // YYYY-MM-DD
	In  *uint64 `json:"in"`
	Out *uint64 `json:"out"`
}

// FlowView is one scope's latest rates and its series over the range.
type FlowView struct {
	Rx     float64 `json:"rx"`
	Tx     float64 `json:"tx"`
	Series *Series `json:"series"` // nil when there was no traffic in the range
}

// Series holds one rate per TrafficView.Times.
type Series struct {
	Rx Values `json:"rx"`
	Tx Values `json:"tx"`
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

// slot is where the rings hold the minute m.
func slot(m int64) int { return int(m % minuteSlots) }

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
		f := t.track(r.Scope, now)
		if f == nil {
			continue
		}
		f.today = pair[uint64]{f.today[0] + r.Rx, f.today[1] + r.Tx}
	}
	return nil
}

// track returns scope's flow, adding it as seen at seen if there is none yet and room for it; nil for a scope
// that is neither the host nor a container.
func (t *Traffic) track(scope string, seen time.Time) *flow {
	f := t.flows[scope]
	if f == nil {
		if !strings.HasPrefix(scope, ctrScope) || len(t.flows) > maxTracked {
			return nil
		}
		f = &flow{seen: seen}
		t.flows[scope] = f
	}
	return f
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
	t.peak = pair[float32]{}
	t.day = day
}

// openMinute makes m the minute being collected, starting a new day when m is in another one.
func (t *Traffic) openMinute(m int64) {
	t.open = m
	t.seen, t.openCPU, t.openMem, t.openHost = false, 0, 0, 0
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

	// Spread the bytes read over the minutes they cover, closing each minute that ended. A minute without a
	// reading of its own was watched when the readings around it are close enough to trust.
	cur := minuteOf(now)
	if t.open == 0 || cur < t.open { // the first reading, or the clock went back
		t.openMinute(cur)
	}
	bridged := !t.last.IsZero() && now.After(t.last) && now.Sub(t.last) <= maxGap
	for t.open < cur {
		t.spread(now)
		t.closeMinute(t.seen || bridged)
		t.openMinute(max(t.open+1, cur-minuteSlots)) // after a long gap only the last day's minutes matter
	}
	t.spread(now)
	t.seen = true

	ms := now.UnixMilli()
	t.liveAt[t.nextLive] = ms
	for key, f := range t.flows {
		f.delta = pair[float64]{}
		f.live[t.nextLive] = pair[float32]{}
		if f.at.Equal(now) {
			f.live[t.nextLive] = f.rate
			if key == hostScope {
				t.peak = pair[float32]{max(t.peak[0], f.rate[0]), max(t.peak[1], f.rate[1])}
			}
		}
		if key != hostScope && now.Sub(f.seen) > trackedFor {
			delete(t.flows, key)
		}
	}
	t.nextLive = (t.nextLive + 1) % liveSlots
	t.last = now
}

// addHost adds a host reading, CPU use in percent and memory used in bytes, to the open minute's averages.
// Call it after observe for the same reading.
func (t *Traffic) addHost(cpu float64, mem uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.open != 0 {
		t.openCPU += cpu
		t.openMem += float64(mem)
		t.openHost++
	}
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

// openRecord is the open minute as the history keeps it: its bytes so far and its host averages.
func (t *Traffic) openRecord() store.HistoryMinute {
	m := t.open * 60
	rec := store.HistoryMinute{Minute: m, Hour: hourStart(m, t.loc), Day: dayStart(m, t.loc)}
	if t.openHost > 0 {
		n := float64(t.openHost)
		rec.Host = &store.HostAvg{CPU: float64(float32(t.openCPU / n)), Mem: uint64(math.Round(t.openMem / n))}
	}
	for key, f := range t.flows {
		b := pair[uint64]{uint64(math.Round(f.open[0])), uint64(math.Round(f.open[1]))}
		if key == hostScope || b != (pair[uint64]{}) {
			rec.Traffic = append(rec.Traffic, store.SourceBytes{Source: key, Rx: b[0], Tx: b[1]})
		}
	}
	return rec
}

// queueOpen queues the open minute for the history unless a minute at or after it was queued already (the
// clock went back): a minute is handed to the history once.
func (t *Traffic) queueOpen() {
	if t.keep && t.open > t.lastQueued {
		t.enqueue(t.openRecord())
		t.lastQueued = t.open
	}
}

// closeMinute records the open minute's averages if it was watched, adds its bytes to today's totals and to
// those waiting to be written, and queues it for the history.
func (t *Traffic) closeMinute(watched bool) {
	m, i := t.open, slot(t.open)
	t.minuteAt[i], t.hostAt[i] = 0, 0
	if watched {
		t.queueOpen()
		t.minuteAt[i] = m
		if t.openHost > 0 {
			n := float64(t.openHost)
			t.hostAt[i], t.cpu[i], t.mem[i] = m, float32(t.openCPU/n), uint64(math.Round(t.openMem/n))
		}
	}
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
}

// flushOpen queues the open minute, as far as it was counted, for the history; on shutdown, so the history
// holds what the daily totals do.
func (t *Traffic) flushOpen() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.open != 0 && t.seen {
		t.queueOpen()
	}
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
	s := TrafficSummary{TodayRx: rx, TodayTx: tx, PeakRx: float64(t.peak[0]), PeakTx: float64(t.peak[1])}
	if t.period != "" {
		s.Budget = &Budget{Period: t.period, Limit: t.limit, Used: rx + tx}
		if t.period == "month" {
			s.Budget.Used += t.month
		}
	}
	return s
}

// lastClosed is the latest closed minute: the one before the open minute, or before now's before the first
// reading.
func (t *Traffic) lastClosed(now time.Time) int64 {
	if t.open != 0 {
		return t.open - 1
	}
	return minuteOf(now) - 1
}

// View returns the traffic over the last five minutes (rng "5m", every reading) or the last day (any other
// rng: the 1440 minutes up to the latest closed one before now, a gap where a minute was not watched). It shows
// at most maxSeries containers, those with the most traffic in the range. The series are copied under the
// lock and built after it.
func (t *Traffic) View(rng string, now time.Time) TrafficView {
	t.mu.Lock()
	v := TrafficView{TrafficSummary: t.summary(), Range: rng, Step: int64(watchedEvery / time.Second),
		Times: []int64{}, Containers: []ContainerTraffic{}}
	live := rng == "5m"
	var idx []int // per time, the ring slot; -1 for a minute not watched
	if live {
		cut := t.last.Add(-historyWindow).UnixMilli()
		for n := range liveSlots {
			if i := (t.nextLive + n) % liveSlots; t.liveAt[i] > cut {
				idx = append(idx, i)
				v.Times = append(v.Times, t.liveAt[i])
			}
		}
	} else {
		v.Range, v.Step = "24h", 60
		last := t.lastClosed(now)
		for m := last - minuteSlots + 1; m <= last; m++ {
			i := slot(m)
			if t.minuteAt[i] != m {
				i = -1
			}
			idx = append(idx, i)
			v.Times = append(v.Times, m*60_000)
		}
	}
	ring := func(f *flow) []pair[float32] {
		if live {
			return f.live[:]
		}
		return f.minutes[:]
	}
	// The containers with the most traffic in the range.
	totals := map[string]float64{}
	var names []string
	for key, f := range t.flows {
		if strings.HasPrefix(key, ctrScope) {
			r, sum := ring(f), 0.0
			for _, i := range idx {
				if i >= 0 {
					sum += float64(r[i][0]) + float64(r[i][1])
				}
			}
			totals[key] = sum
			names = append(names, key)
		}
	}
	slices.SortFunc(names, func(a, b string) int { return cmp.Or(cmp.Compare(totals[b], totals[a]), cmp.Compare(a, b)) })
	names = names[:min(len(names), maxSeries)]

	type taken struct {
		name   string
		rx, tx float64
		today  pair[uint64]
		vals   []pair[float32]
	}
	gap := float32(math.NaN())
	take := func(key string) taken {
		f := t.flows[key]
		tk := taken{name: strings.TrimPrefix(key, ctrScope), vals: make([]pair[float32], len(idx)),
			today: pair[uint64]{f.today[0] + uint64(f.open[0]), f.today[1] + uint64(f.open[1])}}
		tk.rx, tk.tx = t.liveRates(f)
		r := ring(f)
		for n, i := range idx {
			if i < 0 {
				tk.vals[n] = pair[float32]{gap, gap}
			} else {
				tk.vals[n] = r[i]
			}
		}
		return tk
	}
	host := take(hostScope)
	ctrs := make([]taken, len(names))
	for i, key := range names {
		ctrs[i] = take(key)
	}
	t.mu.Unlock()

	view := func(tk taken) FlowView {
		fv := FlowView{Rx: tk.rx, Tx: tk.tx}
		s := &Series{Rx: make(Values, len(tk.vals)), Tx: make(Values, len(tk.vals))}
		for n, p := range tk.vals {
			s.Rx[n], s.Tx[n] = float64(p[0]), float64(p[1])
			if !live {
				s.Rx[n], s.Tx[n] = math.Round(s.Rx[n]), math.Round(s.Tx[n]) // NaN stays NaN
			}
		}
		if s.moves() {
			fv.Series = s
		}
		return fv
	}
	v.Host = view(host)
	for _, tk := range ctrs {
		v.Containers = append(v.Containers, ContainerTraffic{Name: tk.name, FlowView: view(tk),
			TodayRx: tk.today[0], TodayTx: tk.today[1]})
	}
	sortContainers(v.Containers)
	return v
}

// moves reports whether s has any traffic.
func (s *Series) moves() bool {
	for i := range s.Rx {
		if s.Rx[i] > 0 || s.Tx[i] > 0 { // false for NaN
			return true
		}
	}
	return false
}

func sortContainers(cs []ContainerTraffic) {
	slices.SortFunc(cs, func(a, b ContainerTraffic) int {
		return cmp.Or(cmp.Compare(b.TodayRx+b.TodayTx, a.TodayRx+a.TodayTx), cmp.Compare(a.Name, b.Name))
	})
}

// ContainerTraffic is one container's traffic. Service names the service linked to it, if any.
type ContainerTraffic struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	FlowView
	TodayRx uint64 `json:"todayRx"`
	TodayTx uint64 `json:"todayTx"`
}
