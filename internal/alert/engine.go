package alert

import (
	"cmp"
	"slices"
	"time"
)

// Events: what a message says about its alert.
const (
	EventFiring   = "firing"   // the condition has lasted its debounce: it is reported
	EventRepeat   = "repeat"   // it is still firing after the repeat interval
	EventResolved = "resolved" // it is over
	EventSummary  = "summary"  // held or suppressed alerts, summed up
	EventTest     = "test"     // a test the user asked for
)

const (
	// maxEntries bounds the conditions the engine follows; beyond it, new ones are ignored until others end.
	maxEntries = 2000
	// resolveAfter is how long a firing condition must be gone before it is over, so one that flaps is not
	// reported again and again.
	resolveAfter = time.Minute
	// warmUp is how long after a start the alerts restored from before it are not resolved: the data they were
	// read from comes in over the first minute.
	warmUp = time.Minute
)

// Note is one message the engine decided to send.
type Note struct {
	Event string
	Condition
	Since time.Time // when the condition began
}

// Saved is a firing alert as it is kept across restarts.
type Saved struct {
	Condition
	Since            time.Time
	Notified         time.Time // zero: never sent
	NotifiedSeverity Severity  // the severity it was last sent with
}

// entry is one followed condition: pending until it has lasted its debounce, then firing until it is gone.
type entry struct {
	Condition
	since       time.Time // first seen
	gone        time.Time // since when it is missing; zero while present
	firing      bool
	notified    time.Time // last sent; zero when never
	notifiedSev Severity  // the severity last sent: only a worse one is sent again before the repeat interval
	restored    bool      // it was firing before this start
}

// Engine decides, from one evaluation to the next, which conditions are reported, repeated and over. It holds
// no lock: its caller serializes its use.
type Engine struct {
	entries map[string]*entry
	started time.Time
	// Overflow counts the conditions ignored because maxEntries were followed already.
	Overflow int
}

// NewEngine returns an Engine started at now that continues the firing alerts in saved: they are not sent again.
func NewEngine(now time.Time, saved []Saved) *Engine {
	e := &Engine{entries: map[string]*entry{}, started: now}
	for _, s := range saved {
		if len(e.entries) < maxEntries {
			e.entries[s.Key] = &entry{Condition: s.Condition, since: s.Since, firing: true, notified: s.Notified,
				notifiedSev: cmp.Or(s.NotifiedSeverity, s.Severity), restored: true}
		}
	}
	return e
}

// Step takes one evaluation: the conditions now, the scopes that were read and the services that exist. It
// returns the messages to send, the firing alerts to save and the keys of those to forget.
func (e *Engine) Step(now time.Time, r Rules, conds []Condition, known map[string]bool, services map[int64]bool) (
	notes []Note, put []Saved, del []string) {
	present := make(map[string]bool, len(conds))
	for _, c := range conds {
		present[c.Key] = true
		en, ok := e.entries[c.Key]
		if !ok {
			if len(e.entries) >= maxEntries {
				e.Overflow++
				continue
			}
			en = &entry{since: now}
			e.entries[c.Key] = en
		}
		// Worse than what was sent last: a severity that flaps across a line is sent once per new worst.
		worse := en.firing && rank(c.Severity) < rank(en.notifiedSev)
		changed := en.Title != c.Title || en.Message != c.Message || en.Severity != c.Severity
		en.Condition, en.gone = c, time.Time{}
		switch {
		case !en.firing && now.Sub(en.since) >= r.debounce(c.Rule):
			en.firing, en.notified, en.notifiedSev = true, now, c.Severity
			notes = append(notes, Note{Event: EventFiring, Condition: c, Since: en.since})
		case worse:
			en.notified, en.notifiedSev = now, c.Severity
			notes = append(notes, Note{Event: EventFiring, Condition: c, Since: en.since})
		case en.firing && r.RepeatHours > 0 && now.Sub(en.notified) >= time.Duration(r.RepeatHours)*time.Hour:
			en.notified, en.notifiedSev = now, c.Severity
			notes = append(notes, Note{Event: EventRepeat, Condition: c, Since: en.since})
		case !changed || !en.firing:
			continue
		}
		put = append(put, en.saved())
	}

	for key, en := range e.entries {
		if present[key] {
			continue
		}
		switch {
		case !r.on(en.Rule), en.Service != 0 && !services[en.Service]:
			// Switched off, or its service was removed: forgotten without a word.
		case !known[en.Scope], en.restored && now.Sub(e.started) < warmUp:
			continue // not read this time: it may well go on
		case !en.firing:
			// A blip that never lasted its debounce.
		case en.Rule == RuleTraffic:
			// The period ended: there is nothing to recover from.
		default:
			if en.gone.IsZero() {
				en.gone = now
			}
			if now.Sub(en.gone) < resolveAfter {
				continue
			}
			notes = append(notes, Note{Event: EventResolved, Condition: en.Condition, Since: en.since})
		}
		delete(e.entries, key)
		if en.firing {
			del = append(del, key)
		}
	}
	slices.SortStableFunc(notes, func(a, b Note) int {
		return cmp.Or(cmp.Compare(rank(a.Severity), rank(b.Severity)), cmp.Compare(a.Key, b.Key))
	})
	return notes, put, del
}

func (en *entry) saved() Saved {
	return Saved{Condition: en.Condition, Since: en.since, Notified: en.notified, NotifiedSeverity: en.notifiedSev}
}

// Firing returns the firing alerts, worst first, then oldest first.
func (e *Engine) Firing() []Saved {
	out := []Saved{}
	for _, en := range e.entries {
		if en.firing {
			out = append(out, en.saved())
		}
	}
	slices.SortFunc(out, func(a, b Saved) int {
		return cmp.Or(cmp.Compare(rank(a.Severity), rank(b.Severity)), a.Since.Compare(b.Since), cmp.Compare(a.Key, b.Key))
	})
	return out
}

// rank orders severities, worst first.
func rank(s Severity) int {
	switch s {
	case Critical:
		return 0
	case Warning:
		return 1
	}
	return 2
}
