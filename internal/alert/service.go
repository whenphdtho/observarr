package alert

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"observarr/internal/secret"
	"observarr/internal/state"
	"observarr/internal/store"
	"observarr/internal/system"
)

// Config is what a Service reads and sends with.
type Config struct {
	Store    *store.Store
	Box      *secret.Box
	Hub      *state.Hub
	System   func() system.Snapshot // nil: no host or container figures
	Security func() SecurityState   // nil: no sign-in figures
	Deps     Deps
	Log      *slog.Logger
	Now      func() time.Time // nil: time.Now
	Every    time.Duration    // evaluation interval besides changes; 0: 30 s
	Backoff  time.Duration    // first wait before a delivery is tried again; 0: 2 s
}

// Service evaluates the rules on every change of the live state, and every 30 s, and delivers what the engine
// decides on to the enabled channels.
type Service struct {
	cfg  Config
	ctx  context.Context // the Service's lifetime, from Start
	runs sync.WaitGroup  // the evaluation loop and the workers

	mu        sync.Mutex // guards the fields below and the workers' guarded fields
	rules     Rules
	engine    *Engine
	workers   map[int64]*worker
	overrun   int    // Engine.Overflow last logged
	attention []Item // what needs attention now, from the latest evaluation; nil before the first
	watchers  map[chan struct{}]bool
}

// Item is one thing that needs attention now, as the UI lists it: every condition of every rule, before any
// debounce, whether its alert is switched on or not. Firing tells whether it is an alert firing now; Since is when
// the condition began.
type Item struct {
	Key      string    `json:"key"`
	Rule     Rule      `json:"rule"`
	Severity Severity  `json:"severity"`
	Service  int64     `json:"service,omitempty"`
	Name     string    `json:"name"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	View     string    `json:"view"`
	Firing   bool      `json:"firing"`
	Since    time.Time `json:"since"`
}

// New returns a Service; Start starts it.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Every == 0 {
		cfg.Every = 30 * time.Second
	}
	if cfg.Backoff == 0 {
		cfg.Backoff = 2 * time.Second
	}
	return &Service{cfg: cfg, rules: DefaultRules(), workers: map[int64]*worker{}, watchers: map[chan struct{}]bool{}}
}

// savedBody is how a firing alert is kept in the store: its condition and the severity it was last sent with.
type savedBody struct {
	Condition
	NotifiedSeverity Severity `json:"notifiedSeverity,omitempty"`
}

// Start loads the rules, the alerts that were firing before and the channels, then evaluates until ctx ends.
func (s *Service) Start(ctx context.Context) error {
	s.ctx = ctx
	rows, err := s.cfg.Store.AlertStates(ctx)
	if err != nil {
		return err
	}
	saved := make([]Saved, 0, len(rows))
	for _, row := range rows {
		var b savedBody
		if json.Unmarshal(row.Body, &b) == nil && b.Key == row.Key {
			saved = append(saved, Saved{Condition: b.Condition, Since: row.Since, Notified: row.Notified,
				NotifiedSeverity: cmp.Or(b.NotifiedSeverity, b.Severity)})
		}
	}
	s.engine = NewEngine(s.cfg.Now(), saved)
	if err := s.liftTopics(ctx); err != nil {
		return err
	}
	if err := s.Reload(ctx); err != nil {
		return err
	}
	s.runs.Go(func() { s.loop(ctx) })
	return nil
}

// Wait waits until the Service has stopped after its context ended: no delivery is under way any more.
func (s *Service) Wait() { s.runs.Wait() }

// liftTopics keeps the topic of ntfy channels saved before it became a secret among their secrets.
func (s *Service) liftTopics(ctx context.Context) error {
	chans, err := s.cfg.Store.AlertChannels(ctx)
	if err != nil {
		return err
	}
	for _, ch := range chans {
		settings, err := ParseSettings(ch.Settings)
		if err != nil {
			continue
		}
		sec, err := OpenSecrets(s.cfg.Box, ch.ID, ch.Secrets)
		if err != nil {
			continue // its secrets must be entered again anyway, the topic with them
		}
		settings, sec, lifted := LiftTopic(ch.Type, settings, sec)
		if !lifted {
			continue
		}
		next := ch
		if next.Settings, err = json.Marshal(settings); err != nil {
			return err
		}
		if next.Secrets, err = SealSecrets(s.cfg.Box, ch.ID, sec); err != nil {
			return err
		}
		if err := s.cfg.Store.UpdateAlertChannel(ctx, next, ch); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) loop(ctx context.Context) {
	tick := time.NewTicker(s.cfg.Every)
	defer tick.Stop()
	var soon <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.cfg.Hub.Changed():
			// Changes come in bursts (every reading of every service): one evaluation a second is enough.
			if soon == nil {
				soon = time.After(time.Second)
			}
			continue
		case <-soon:
			soon = nil
		case <-tick.C:
		}
		s.evaluate(ctx)
	}
}

// evaluate reads the live state, steps the engine, saves what changed and sends what it decided on.
func (s *Service) evaluate(ctx context.Context) {
	now := s.cfg.Now()
	in := Input{Now: now, Services: s.cfg.Hub.Snapshot()}
	if s.cfg.System != nil {
		in.System = s.cfg.System()
	}
	if s.cfg.Security != nil {
		in.Security = s.cfg.Security()
	}
	services := make(map[int64]bool, len(in.Services))
	for _, svc := range in.Services {
		services[svc.ID] = true
	}

	s.mu.Lock()
	r := s.rules
	// Every rule is evaluated for the attention list; the engine gets the ones switched on.
	all, known := Evaluate(in, r.everyRule())
	conds := slices.DeleteFunc(slices.Clone(all), func(c Condition) bool { return !r.on(c.Rule) })
	notes, put, del := s.engine.Step(now, r, conds, known, services)
	overflow := s.engine.Overflow
	logged := s.overrun
	s.overrun = overflow
	s.setAttention(all)
	s.mu.Unlock()
	if overflow > logged {
		s.cfg.Log.Warn("too many alert conditions at once; some are ignored", "ignored", overflow-logged)
	}

	if err := s.save(ctx, put, del); err != nil && ctx.Err() == nil {
		s.cfg.Log.Warn("saving the alert state failed", "err", err)
	}
	for _, n := range notes {
		s.dispatch(ctx, now, n.Rule, Message{Event: n.Event, Rule: n.Rule, Severity: n.Severity, Name: n.Name,
			Title: n.Title, Text: n.Message, Time: now, URL: link(r.Link, n.View)})
	}
	s.flush(ctx, now)
}

// setAttention keeps the attention list of conds, worst first, and tells the watchers when it changed. Callers
// hold s.mu.
func (s *Service) setAttention(conds []Condition) {
	firing := map[string]Saved{}
	for _, a := range s.engine.Firing() {
		firing[a.Key] = a
	}
	items := make([]Item, len(conds))
	for i, c := range conds {
		f, ok := firing[c.Key]
		items[i] = Item{Key: c.Key, Rule: c.Rule, Severity: c.Severity, Service: c.Service, Name: c.Name,
			Title: c.Title, Message: c.Message, View: c.View, Firing: ok, Since: f.Since}
	}
	slices.SortStableFunc(items, func(a, b Item) int { return cmp.Compare(rank(a.Severity), rank(b.Severity)) })
	if s.attention != nil && slices.Equal(items, s.attention) {
		return
	}
	s.attention = items
	for ch := range s.watchers {
		select {
		case ch <- struct{}{}:
		default: // a signal is waiting already
		}
	}
}

// Attention returns what needs attention now, worst first; nil until the first evaluation, when nothing is known.
func (s *Service) Attention() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attention
}

// WatchAttention returns a channel that holds a signal when the attention list changed since it was last
// received, and a function that stops the watch.
func (s *Service) WatchAttention() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.watchers[ch] = true
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.watchers, ch)
		s.mu.Unlock()
	}
}

func (s *Service) save(ctx context.Context, put []Saved, del []string) error {
	rows := make([]store.AlertState, 0, len(put))
	for _, a := range put {
		body, err := json.Marshal(savedBody{Condition: a.Condition, NotifiedSeverity: a.NotifiedSeverity})
		if err != nil {
			return err
		}
		rows = append(rows, store.AlertState{Key: a.Key, Body: body, Since: a.Since, Notified: a.Notified})
	}
	return s.cfg.Store.SaveAlertStates(ctx, rows, del)
}

// link is the address of view in Observarr at base, "" without a base.
func link(base, view string) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/" + view
}

// dispatch logs m and hands it to every enabled channel that receives rule, through its gate.
func (s *Service) dispatch(ctx context.Context, now time.Time, rule Rule, m Message) {
	entry := store.AlertLogEntry{At: now, Event: m.Event, Rule: string(rule), Severity: string(m.Severity),
		Service: m.Name, Title: m.Title, Message: m.Text}
	var queued []*worker
	s.mu.Lock()
	for _, w := range s.sortedWorkers() {
		if !slices.Contains(w.rules, rule) {
			continue
		}
		status := w.gate.admit(now, s.rules, m)
		if status == StatusQueued {
			queued = append(queued, w)
		}
		entry.Deliveries = append(entry.Deliveries, store.AlertDelivery{ChannelID: w.id, Channel: w.name,
			Status: status, At: now})
	}
	s.mu.Unlock()
	s.enqueue(ctx, entry, m, queued)
}

// flush sends each channel's summaries that are due.
func (s *Service) flush(ctx context.Context, now time.Time) {
	type summary struct {
		w *worker
		m Message
	}
	var due []summary
	s.mu.Lock()
	for _, w := range s.sortedWorkers() {
		for _, m := range w.gate.due(now, s.rules) {
			due = append(due, summary{w, m})
		}
	}
	s.mu.Unlock()
	for _, d := range due {
		entry := store.AlertLogEntry{At: now, Event: d.m.Event, Severity: string(d.m.Severity), Title: d.m.Title,
			Message: d.m.Text, Deliveries: []store.AlertDelivery{{ChannelID: d.w.id, Channel: d.w.name,
				Status: StatusQueued, At: now}}}
		s.enqueue(ctx, entry, d.m, []*worker{d.w})
	}
}

// enqueue writes entry to the log and queues m on each of queued, marking it dropped where a queue is full.
func (s *Service) enqueue(ctx context.Context, entry store.AlertLogEntry, m Message, queued []*worker) {
	id, err := s.cfg.Store.AddAlertLog(ctx, entry)
	if err != nil && ctx.Err() == nil {
		s.cfg.Log.Warn("writing the alert log failed", "err", err)
	}
	for _, w := range queued {
		if w.ctx.Err() != nil {
			s.setDelivery(w, id, StatusDropped, 0, s.stopped())
			continue
		}
		select {
		case w.jobs <- job{logID: id, m: m}:
		default:
			s.record(w, id, StatusDropped, 0, "the channel's queue is full", false)
		}
	}
}

// sortedWorkers returns the workers by channel id, so log rows list channels in a stable order. Callers hold s.mu.
func (s *Service) sortedWorkers() []*worker {
	return slices.SortedFunc(maps.Values(s.workers), func(a, b *worker) int { return cmp.Compare(a.id, b.id) })
}

// work delivers w's messages until w is stopped or the Service ends; what is still queued then is dropped.
func (s *Service) work(w *worker) {
	for {
		select {
		case <-w.ctx.Done():
			for {
				select {
				case j := <-w.jobs:
					s.setDelivery(w, j.logID, StatusDropped, 0, s.stopped())
				default:
					return
				}
			}
		case j := <-w.jobs:
			if w.ctx.Err() != nil {
				s.setDelivery(w, j.logID, StatusDropped, 0, s.stopped())
				continue
			}
			s.deliver(w, j)
		}
	}
}

// stopped says why a worker stopped: Observarr stops, or its channel was switched off or removed.
func (s *Service) stopped() string {
	if s.ctx.Err() != nil {
		return "Observarr stopped before it was sent"
	}
	return "the channel was switched off or removed"
}

// deliver sends one message, trying again with back-off when that may help, and records what became of it.
func (s *Service) deliver(w *worker, j job) {
	s.mu.Lock()
	sender, secrets := w.sender, w.secrets
	s.mu.Unlock()
	var err error
	n := 1
	for ; ; n++ {
		ctx, cancel := context.WithTimeout(w.ctx, sendTimeout)
		err = sender.Send(ctx, j.m)
		cancel()
		if err == nil || permanent(err) || n == attempts {
			break
		}
		select {
		case <-time.After(backoff(s.cfg.Backoff, n)):
		case <-w.ctx.Done():
		}
		if w.ctx.Err() != nil {
			break
		}
	}
	if err != nil && w.ctx.Err() != nil { // cut short: not the channel's fault
		s.setDelivery(w, j.logID, StatusDropped, n, s.stopped())
		return
	}
	status := StatusSent
	if err != nil {
		status = StatusFailed
	}
	note := describe(err, secrets)
	if err != nil {
		s.cfg.Log.Warn("alert delivery failed", "channel", w.id, "attempts", n, "status", note)
	}
	s.record(w, j.logID, status, n, note, err == nil)
}

// record writes what became of log entry logID on w's channel, and the channel's latest result.
func (s *Service) record(w *worker, logID int64, status string, n int, note string, ok bool) {
	s.setDelivery(w, logID, status, n, note)
	if err := s.cfg.Store.SetAlertChannelResult(context.WithoutCancel(s.ctx), w.id, s.cfg.Now(), ok, note, false,
		nil, nil); err != nil {
		s.cfg.Log.Warn("writing the channel's result failed", "err", err)
	}
}

// setDelivery writes what became of log entry logID on w's channel.
func (s *Service) setDelivery(w *worker, logID int64, status string, n int, note string) {
	if logID == 0 {
		return
	}
	s.mu.Lock()
	name := w.name
	s.mu.Unlock()
	if err := s.cfg.Store.SetAlertDelivery(context.WithoutCancel(s.ctx), logID, store.AlertDelivery{ChannelID: w.id,
		Channel: name, Status: status, Attempts: n, Note: note, At: s.cfg.Now()}); err != nil {
		s.cfg.Log.Warn("writing the alert log failed", "err", err)
	}
}

// Reload reads the rules and the channels again, and starts, changes or stops the channels' workers: call it after
// either changed. A worker keeps its hourly count and held alerts across a change of its channel.
func (s *Service) Reload(ctx context.Context) error {
	rules := DefaultRules()
	body, err := s.cfg.Store.AlertRules(ctx)
	if err != nil {
		return err
	}
	if body != nil {
		if rules, err = ParseRules(body); err != nil {
			s.cfg.Log.Warn("the saved alert rules are invalid; using the defaults", "err", err)
			rules = DefaultRules()
		}
	}
	chans, err := s.cfg.Store.AlertChannels(ctx)
	if err != nil {
		return err
	}
	type ready struct {
		ch      store.AlertChannel
		sender  Sender
		secrets []string
	}
	var active []ready
	for _, ch := range chans {
		if !ch.Enabled {
			continue
		}
		sender, sec, err := s.senderOf(ch)
		if err != nil {
			s.cfg.Log.Warn("alert channel cannot send; enter its settings again", "channel", ch.ID, "err", err)
			continue
		}
		active = append(active, ready{ch, sender, sec.redacted()})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.rules = rules
	keep := map[int64]bool{}
	for _, a := range active {
		keep[a.ch.ID] = true
		w, ok := s.workers[a.ch.ID]
		if !ok {
			w = &worker{id: a.ch.ID, jobs: make(chan job, queueSize)}
			w.ctx, w.cancel = context.WithCancel(s.ctx)
			s.workers[a.ch.ID] = w
			s.runs.Go(func() { s.work(w) })
		}
		w.name, w.sender, w.secrets = a.ch.Name, a.sender, a.secrets
		w.rules = make([]Rule, len(a.ch.Rules))
		for i, r := range a.ch.Rules {
			w.rules[i] = Rule(r)
		}
	}
	for id, w := range s.workers {
		if !keep[id] {
			w.cancel()
			delete(s.workers, id)
		}
	}
	return nil
}

// senderOf builds the sender of a saved channel.
func (s *Service) senderOf(ch store.AlertChannel) (Sender, Secrets, error) {
	settings, err := ParseSettings(ch.Settings)
	if err != nil {
		return nil, nil, err
	}
	sec, err := OpenSecrets(s.cfg.Box, ch.ID, ch.Secrets)
	if err != nil {
		return nil, nil, err
	}
	if settings, err = CheckChannel(ch.Type, settings, sec); err != nil {
		return nil, nil, err
	}
	return NewSender(ch.Type, settings, sec, s.cfg.Deps), sec, nil
}

// Test sends a test message to ch once, now, and records the result: a channel that passes may be enabled. It
// returns whether it got through and the status line.
func (s *Service) Test(ctx context.Context, ch store.AlertChannel) (bool, string) {
	now := s.cfg.Now()
	sender, sec, err := s.senderOf(ch)
	if err == nil {
		m := Message{Event: EventTest, Severity: Info, Name: ch.Name, Time: now, Title: "Test from Observarr",
			Text: "Alerts reach you here.", URL: link(s.Rules().Link, "#/system/alerts")}
		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		err = sender.Send(sendCtx, m)
		cancel()
	}
	note := describe(err, sec.redacted())
	status := StatusSent
	if err != nil {
		status = StatusFailed
	}
	bg := context.WithoutCancel(ctx)
	if _, lerr := s.cfg.Store.AddAlertLog(bg, store.AlertLogEntry{At: now, Event: EventTest, Severity: string(Info),
		Service: ch.Name, Title: "Test from Observarr", Message: "Sent by hand to check the channel", Deliveries: []store.AlertDelivery{{
			ChannelID: ch.ID, Channel: ch.Name, Status: status, Attempts: 1, Note: note, At: now}}}); lerr != nil {
		s.cfg.Log.Warn("writing the alert log failed", "err", lerr)
	}
	if rerr := s.cfg.Store.SetAlertChannelResult(bg, ch.ID, now, err == nil, note, err == nil, ch.Settings,
		ch.Secrets); rerr != nil {
		s.cfg.Log.Warn("writing the channel's result failed", "err", rerr)
	}
	return err == nil, note
}

// Rules returns the rules in force.
func (s *Service) Rules() Rules {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rules
}

// SetRules saves r, which must be checked, and puts it in force.
func (s *Service) SetRules(ctx context.Context, r Rules) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := s.cfg.Store.SetAlertRules(ctx, body); err != nil {
		return err
	}
	s.mu.Lock()
	s.rules = r
	s.mu.Unlock()
	return nil
}
