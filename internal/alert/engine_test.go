package alert

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.Local)

func down(id int64) Condition {
	return Condition{Key: "down/" + string(rune('0'+id)), Rule: RuleDown, Severity: Critical, Scope: scope(id, "down"),
		Service: id, Name: "Sintel", Title: "Sintel is down", Message: "connection refused"}
}

// stepper drives an engine through evaluations at chosen times.
type stepper struct {
	t        *testing.T
	e        *Engine
	r        Rules
	services map[int64]bool
	known    map[string]bool
	saved    map[string]Saved
}

func newStepper(t *testing.T, saved ...Saved) *stepper {
	st := &stepper{t: t, e: NewEngine(t0, saved), r: DefaultRules(), services: map[int64]bool{1: true, 2: true},
		known: map[string]bool{scope(1, "down"): true, scope(2, "down"): true, "traffic": true}, saved: map[string]Saved{}}
	for _, s := range saved {
		st.saved[s.Key] = s
	}
	return st
}

// at evaluates at t0+d with conds and returns the events sent, keeping what the engine saves as a store would.
func (st *stepper) at(d time.Duration, conds ...Condition) []string {
	st.t.Helper()
	notes, put, del := st.e.Step(t0.Add(d), st.r, conds, st.known, st.services)
	for _, s := range put {
		st.saved[s.Key] = s
	}
	for _, k := range del {
		delete(st.saved, k)
	}
	var out []string
	for _, n := range notes {
		out = append(out, n.Event+" "+n.Key)
	}
	return out
}

func (st *stepper) want(got []string, want ...string) {
	st.t.Helper()
	if len(got) != len(want) {
		st.t.Fatalf("sent %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			st.t.Fatalf("sent %q, want %q", got, want)
		}
	}
}

func TestDebounceWaitsTheMinutesSet(t *testing.T) {
	st := newStepper(t)
	c := down(1)
	st.want(st.at(0, c))
	st.want(st.at(2*time.Minute+59*time.Second, c))
	st.want(st.at(3*time.Minute, c), "firing down/1")
	if len(st.saved) != 1 {
		t.Fatalf("saved = %v, want the firing alert", st.saved)
	}
}

func TestBlipShorterThanTheDebounceSendsNothing(t *testing.T) {
	st := newStepper(t)
	st.want(st.at(0, down(1)))
	st.want(st.at(time.Minute))            // up again: forgotten
	st.want(st.at(2*time.Minute, down(1))) // down again: the wait starts over
	st.want(st.at(4*time.Minute, down(1))) // 2 minutes in
	st.want(st.at(5*time.Minute, down(1)), "firing down/1")
}

func TestRecoveryIsSentOnceItLasts(t *testing.T) {
	st := newStepper(t)
	st.r.DownMinutes = 0
	st.want(st.at(0, down(1)), "firing down/1")
	st.want(st.at(30 * time.Second))
	st.want(st.at(40*time.Second, down(1))) // back within a minute: still the same alert, nothing sent
	st.want(st.at(50 * time.Second))
	st.want(st.at(110*time.Second), "resolved down/1")
	if len(st.saved) != 0 {
		t.Fatalf("saved = %v after recovery", st.saved)
	}
}

func TestSameConditionIsSentOnce(t *testing.T) {
	st := newStepper(t)
	st.r.DownMinutes = 0
	st.want(st.at(0, down(1)), "firing down/1")
	for i := 1; i < 100; i++ {
		c := down(1)
		c.Message = "a changing error text" // the same problem: the key is what counts
		st.want(st.at(time.Duration(i)*time.Minute, c))
	}
	if st.saved["down/1"].Message != "a changing error text" {
		t.Fatal("the latest text was not saved")
	}
}

func TestRepeatAfterTheInterval(t *testing.T) {
	st := newStepper(t)
	st.r.DownMinutes, st.r.RepeatHours = 0, 6
	st.want(st.at(0, down(1)), "firing down/1")
	st.want(st.at(5*time.Hour+59*time.Minute, down(1)))
	st.want(st.at(6*time.Hour, down(1)), "repeat down/1")
	st.want(st.at(11*time.Hour, down(1)))
	st.want(st.at(12*time.Hour, down(1)), "repeat down/1")

	st.r.RepeatHours = 0 // off: never again
	st.want(st.at(48*time.Hour, down(1)))
}

func TestWorseSeverityIsSentAgain(t *testing.T) {
	st := newStepper(t)
	c := Condition{Key: "disk//", Rule: RuleStorage, Severity: Warning, Scope: "host", Title: "Disk / nearly full"}
	st.known["host"] = true
	st.want(st.at(0, c))
	st.want(st.at(5*time.Minute, c), "firing disk//")
	c.Severity = Critical
	st.want(st.at(6*time.Minute, c), "firing disk//")
	c.Severity = Warning // better again: nothing to say until it is over
	st.want(st.at(7*time.Minute, c))
}

func TestUnreadScopeIsNeitherOverNorForgotten(t *testing.T) {
	st := newStepper(t)
	st.r.DownMinutes = 0
	st.want(st.at(0, down(1)), "firing down/1")
	delete(st.known, scope(1, "down")) // not checked this time
	st.want(st.at(time.Hour))
	st.known[scope(1, "down")] = true
	st.want(st.at(time.Hour + time.Minute))
	st.want(st.at(time.Hour+2*time.Minute), "resolved down/1")
}

func TestRemovedServiceAndDisabledRuleEndQuietly(t *testing.T) {
	st := newStepper(t)
	st.r.DownMinutes = 0
	st.want(st.at(0, down(1), down(2)), "firing down/1", "firing down/2")
	delete(st.services, 1)
	st.want(st.at(time.Minute, down(2)))
	st.r.Enabled[RuleDown] = false
	st.want(st.at(2 * time.Minute))
	st.want(st.at(10 * time.Minute))
	if len(st.saved) != 0 {
		t.Fatalf("saved = %v", st.saved)
	}
}

func TestTrafficPeriodEndsQuietly(t *testing.T) {
	st := newStepper(t)
	c := Condition{Key: "traffic/80/2026-11-26", Rule: RuleTraffic, Severity: Warning, Scope: "traffic"}
	st.want(st.at(0, c), "firing traffic/80/2026-11-26")
	st.want(st.at(time.Hour)) // the next day: the budget is fresh, and that needs no message
	st.want(st.at(2 * time.Hour))
	if len(st.saved) != 0 {
		t.Fatalf("saved = %v", st.saved)
	}
}

// A restart continues the firing alerts it saved: nothing is sent again, not even while the data the alerts were
// read from is still coming in, and they recover as usual afterwards.
func TestRestartSendsNothingAgain(t *testing.T) {
	before := newStepper(t)
	before.r.DownMinutes = 0
	before.want(before.at(0, down(1), down(2)), "firing down/1", "firing down/2")

	var saved []Saved
	for _, s := range before.saved {
		saved = append(saved, s)
	}
	after := newStepper(t, saved...)
	after.r.DownMinutes = 0
	after.want(after.at(0, down(2))) // service 1's first check is not in yet
	after.want(after.at(10*time.Second, down(1), down(2)))
	after.want(after.at(20*time.Second, down(2))) // service 1 is up, but it is still the first minute
	after.want(after.at(time.Minute, down(2)))
	after.want(after.at(2*time.Minute, down(2)), "resolved down/1")
	if len(after.e.Firing()) != 1 {
		t.Fatalf("firing = %v", after.e.Firing())
	}
}

func TestEntriesAreBounded(t *testing.T) {
	st := newStepper(t)
	conds := make([]Condition, maxEntries+10)
	for i := range conds {
		conds[i] = Condition{Key: "k" + string(rune(i)), Rule: RuleSecurity, Severity: Warning, Scope: "security"}
	}
	st.known["security"] = true
	if got := st.at(0, conds...); len(got) != maxEntries || st.e.Overflow != 10 {
		t.Fatalf("sent %d, overflow %d; want %d and 10", len(got), st.e.Overflow, maxEntries)
	}
}
