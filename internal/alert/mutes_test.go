package alert

import (
	"context"
	"errors"
	"testing"
	"time"

	"observarr/internal/collect"
	"observarr/internal/store"
	"observarr/internal/system"
)

// item returns the attention item with key, or fails.
func (e *env) item(key string) Item {
	e.t.Helper()
	for _, it := range e.alert.Attention() {
		if it.Key == key {
			return it
		}
	}
	e.t.Fatalf("nothing with key %q in %+v", key, e.alert.Attention())
	return Item{}
}

func (e *env) events() []string {
	var out []string
	for _, l := range e.log() {
		out = append(out, l.Event+" "+l.Severity+" "+l.Title)
	}
	return out
}

func (e *env) savedMutes() int {
	e.t.Helper()
	m, err := e.st.AlertMutes(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return len(m)
}

func (e *env) eval(after time.Duration) {
	e.advance(after)
	e.alert.evaluate(context.Background())
}

func TestMutedConditionSendsNothingAndStaysListed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	r := DefaultRules()
	r.DownMinutes = 1
	e.start(r)
	e.down(1)
	e.eval(0)
	if _, err := e.alert.Mute(ctx, "down/7", time.Hour, "alex"); !errors.Is(err, ErrNotListed) {
		t.Fatalf("muting a key nothing has: %v", err)
	}
	until, err := e.alert.Mute(ctx, "down/1", time.Hour, "alex")
	if err != nil || !until.Equal(e.clock().Add(time.Hour)) {
		t.Fatalf("mute = %v, %v", until, err)
	}
	if it := e.item("down/1"); !it.MutedUntil.Equal(until) {
		t.Fatalf("item = %+v, want it muted until %v", it, until)
	}
	// Its debounce passes while it is muted: nothing is sent, for as long as the mute lasts.
	e.eval(5 * time.Minute)
	e.eval(50 * time.Minute)
	if got := e.events(); len(got) != 0 {
		t.Fatalf("sent while muted: %q", got)
	}

	// The mute ends on its own: the condition is still there, so it is reported now.
	e.eval(5 * time.Minute)
	if got := e.events(); len(got) != 1 || got[0] != "firing critical Sintel is not reachable" {
		t.Fatalf("after the mute ended: %q", got)
	}
	if it := e.item("down/1"); !it.MutedUntil.IsZero() || !it.Firing || e.savedMutes() != 0 {
		t.Fatalf("item = %+v, %d mutes saved", it, e.savedMutes())
	}
}

func TestUnmuteReportsAtOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.start(quick())
	e.down(1)
	e.eval(0) // fires at once
	e.down(2)
	if _, err := e.alert.Mute(ctx, "down/1", 8*time.Hour, "alex"); err != nil {
		t.Fatal(err)
	}
	if err := e.alert.Unmute(ctx, "down/1"); err != nil {
		t.Fatal(err)
	}
	if err := e.alert.Unmute(ctx, "down/1"); err != nil { // no mute: nothing to do
		t.Fatal(err)
	}
	if it := e.item("down/1"); !it.MutedUntil.IsZero() || e.savedMutes() != 0 {
		t.Fatalf("after unmuting: %+v", it)
	}
}

func TestConditionOverWhileMutedEndsTheMuteQuietly(t *testing.T) {
	e := newEnv(t)
	e.start(quick())
	e.down(1)
	e.eval(0)
	if _, err := e.alert.Mute(context.Background(), "down/1", 24*time.Hour, "alex"); err != nil {
		t.Fatal(err)
	}
	e.up(1)
	e.eval(time.Second)
	e.eval(2 * time.Minute)
	e.eval(2 * time.Minute)
	if got := e.events(); len(got) != 1 || got[0] != "firing critical Sintel is not reachable" {
		t.Fatalf("sent = %q, want no \"resolved\" for a muted alert", got)
	}
	if n := e.savedMutes(); n != 0 || len(e.alert.Attention()) != 0 {
		t.Fatalf("%d mutes saved, attention %+v", n, e.alert.Attention())
	}
	if states, _ := e.st.AlertStates(context.Background()); len(states) != 0 {
		t.Fatalf("the forgotten alert is still saved: %+v", states)
	}
	// Down again later: a new alert, reported as any other.
	e.down(1)
	e.eval(time.Minute)
	if got := e.events(); len(got) != 2 {
		t.Fatalf("down again after the mute ended: %q", got)
	}
}

func TestWorseSeverityBreaksTheMute(t *testing.T) {
	e := newEnv(t)
	var free uint64
	e.sys = func() system.Snapshot {
		e.mu.Lock()
		defer e.mu.Unlock()
		return system.Snapshot{At: e.now, Notices: []string{system.NoticeDocker},
			Host: &system.HostView{Disks: []collect.Disk{{Path: "/media", Total: 1000, Free: free, Avail: free}}}}
	}
	disk := func(avail uint64) {
		e.mu.Lock()
		free = avail
		e.mu.Unlock()
	}
	disk(60) // 94% used: a warning
	e.start(DefaultRules())
	e.eval(0)
	e.eval(6 * time.Minute)
	if got := e.events(); len(got) != 1 || got[0] != "firing warning Disk /media nearly full" {
		t.Fatalf("sent = %q", got)
	}
	if _, err := e.alert.Mute(context.Background(), "disk//media", 24*time.Hour, "alex"); err != nil {
		t.Fatal(err)
	}
	disk(55) // still a warning: still muted
	e.eval(time.Minute)
	if it := e.item("disk//media"); it.MutedUntil.IsZero() {
		t.Fatalf("a warning that stays a warning lost its mute: %+v", it)
	}
	disk(10) // 99%: critical
	e.eval(time.Minute)
	if got := e.events(); len(got) != 2 || got[0] != "firing critical Disk /media nearly full" {
		t.Fatalf("after it got worse: %q", got)
	}
	if it := e.item("disk//media"); !it.MutedUntil.IsZero() || e.savedMutes() != 0 {
		t.Fatalf("still muted after getting worse: %+v", it)
	}
}

// The severity a mute was set at is saved with it: what counts as worse is the same after a restart.
func TestWorseSeverityBreaksTheMuteAfterARestart(t *testing.T) {
	e := newEnv(t)
	var free uint64 = 60
	e.sys = func() system.Snapshot {
		e.mu.Lock()
		defer e.mu.Unlock()
		return system.Snapshot{At: e.now, Notices: []string{system.NoticeDocker},
			Host: &system.HostView{Disks: []collect.Disk{{Path: "/media", Total: 1000, Free: free, Avail: free}}}}
	}
	e.start(DefaultRules())
	e.eval(0)
	e.eval(6 * time.Minute)
	if _, err := e.alert.Mute(context.Background(), "disk//media", 24*time.Hour, "alex"); err != nil {
		t.Fatal(err)
	}
	if m, _ := e.st.AlertMutes(context.Background()); len(m) != 1 || m[0].Severity != "warning" {
		t.Fatalf("saved mute = %+v", m)
	}
	e.stop()

	// It is critical by the time Observarr is back: the very first reading breaks the mute.
	e.mu.Lock()
	free = 10
	e.mu.Unlock()
	e.advance(time.Minute)
	e.start(DefaultRules())
	e.eval(0)
	if it := e.item("disk//media"); !it.MutedUntil.IsZero() || e.savedMutes() != 0 {
		t.Fatalf("still muted after getting worse across a restart: %+v", it)
	}
	if got := e.events(); len(got) != 2 || got[0] != "firing critical Disk /media nearly full" {
		t.Fatalf("sent = %q", got)
	}
}

// A condition whose rule is switched off sends nothing anyway: muting it is accepted, shows, and simply ends.
func TestMutingAConditionWhoseRuleIsOff(t *testing.T) {
	e := newEnv(t)
	r := quick()
	r.Enabled[RuleDown] = false
	e.start(r)
	e.down(1)
	e.eval(0)
	until, err := e.alert.Mute(context.Background(), "down/1", time.Hour, "alex")
	if err != nil {
		t.Fatalf("muting a condition whose rule is off: %v", err)
	}
	if it := e.item("down/1"); !it.MutedUntil.Equal(until) || it.Firing {
		t.Fatalf("item = %+v", it)
	}
	e.eval(30 * time.Minute)
	if e.savedMutes() != 1 {
		t.Fatal("the mute went before its time")
	}
	e.eval(31 * time.Minute)
	if it := e.item("down/1"); !it.MutedUntil.IsZero() || e.savedMutes() != 0 || len(e.events()) != 0 {
		t.Fatalf("after its time: %+v, %d saved, sent %q", it, e.savedMutes(), e.events())
	}
}

func TestMuteSurvivesARestart(t *testing.T) {
	e := newEnv(t)
	e.start(quick())
	e.down(1)
	e.down(2)
	e.eval(0)
	until, err := e.alert.Mute(context.Background(), "down/1", 8*time.Hour, "alex")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetAlertMute(context.Background(), store.AlertMute{Key: "down/9", Until: e.clock().Add(-time.Minute), Severity: "critical", CreatedBy: "alex"}); err != nil {
		t.Fatal(err)
	}
	e.stop()

	e.advance(10 * time.Minute)
	r := quick()
	r.RepeatHours = 1
	e.start(r)
	e.eval(0)
	if it := e.item("down/1"); !it.MutedUntil.Equal(until) {
		t.Fatalf("after the restart: %+v, want muted until %v", it, until)
	}
	if it := e.item("down/2"); !it.MutedUntil.IsZero() {
		t.Fatalf("the other alert got muted: %+v", it)
	}
	// The repeat interval passes: only the one that is not muted is sent again.
	e.eval(time.Hour)
	var repeats []string
	for _, ev := range e.events() {
		if ev[:6] == "repeat" {
			repeats = append(repeats, ev)
		}
	}
	if len(repeats) != 1 || repeats[0] != "repeat critical Caminandes is not reachable" {
		t.Fatalf("repeats = %q", repeats)
	}
	// A mute whose condition never shows again after a start is dropped once the data has come in.
	e.up(1)
	e.eval(2 * time.Minute)
	if n := e.savedMutes(); n != 1 { // the ended one of down/9 is left to upkeep
		t.Fatalf("%d mutes saved", n)
	}
}
