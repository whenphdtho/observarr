package alert

import (
	"strings"
	"testing"
	"time"
)

func msg(sev Severity, title string) Message {
	return Message{Event: EventFiring, Severity: sev, Title: title}
}

func TestRateLimitThenOneSummary(t *testing.T) {
	r := DefaultRules()
	r.PerHour = 3
	var g gate
	var statuses []string
	for i := range 10 {
		statuses = append(statuses, g.admit(t0.Add(time.Duration(i)*time.Second), r, msg(Critical, "x")))
	}
	if strings.Join(statuses, " ") != "queued queued queued suppressed suppressed suppressed suppressed suppressed suppressed suppressed" {
		t.Fatalf("statuses = %v", statuses)
	}
	if due := g.due(t0.Add(30*time.Minute), r); len(due) != 0 {
		t.Fatalf("a summary while the hour is used up: %v", due)
	}
	due := g.due(t0.Add(time.Hour+time.Second), r)
	if len(due) != 1 || due[0].Event != EventSummary || due[0].Title != "7 more alerts suppressed" {
		t.Fatalf("due = %+v, want one summary of 7", due)
	}
	if due := g.due(t0.Add(time.Hour+2*time.Second), r); len(due) != 0 {
		t.Fatalf("a second summary: %v", due)
	}
	// The summary took one of the hour's messages: two are left.
	if g.admit(t0.Add(time.Hour+3*time.Second), r, msg(Info, "a")) != StatusQueued ||
		g.admit(t0.Add(time.Hour+4*time.Second), r, msg(Info, "b")) != StatusQueued ||
		g.admit(t0.Add(time.Hour+5*time.Second), r, msg(Info, "c")) != StatusSuppressed {
		t.Fatal("the hour after the summary does not count it")
	}
}

func TestQuietHoursHoldAllButCritical(t *testing.T) {
	r := DefaultRules()
	r.Quiet = Quiet{On: true, From: "22:00", To: "07:00"}
	night := time.Date(2026, 11, 26, 23, 30, 0, 0, time.Local)
	var g gate
	if s := g.admit(night, r, msg(Warning, "Disk / nearly full")); s != StatusHeld {
		t.Fatalf("a warning at night: %s", s)
	}
	if s := g.admit(night, r, msg(Critical, "Sintel is down")); s != StatusQueued {
		t.Fatalf("a critical alert at night: %s", s)
	}
	for i := range maxHeld + 5 {
		g.admit(night.Add(time.Duration(i)*time.Minute), r, msg(Info, "requests waiting"))
	}
	if due := g.due(night.Add(6*time.Hour), r); len(due) != 0 { // 05:30: still quiet
		t.Fatalf("summary during quiet hours: %v", due)
	}
	due := g.due(time.Date(2026, 11, 27, 7, 0, 0, 0, time.Local), r)
	if len(due) != 1 || due[0].Title != "26 alerts during quiet hours" || due[0].Severity != Warning ||
		!strings.Contains(due[0].Text, "• Disk / nearly full") || !strings.HasSuffix(due[0].Text, "and 6 more") {
		t.Fatalf("due = %+v", due)
	}
}

func TestQuietHoursWithinOneDay(t *testing.T) {
	r := DefaultRules()
	r.Quiet = Quiet{On: true, From: "13:00", To: "14:00"}
	for h, want := range map[int]bool{12: false, 13: true, 14: false} {
		if got := r.quietAt(time.Date(2026, 11, 26, h, 0, 0, 0, time.Local)); got != want {
			t.Errorf("%d:00 quiet = %v", h, got)
		}
	}
}

func TestBackoffGrowsWithJitter(t *testing.T) {
	for n := 1; n <= 3; n++ {
		base := time.Second << (n - 1)
		for range 50 {
			if d := backoff(time.Second, n); d < base/2 || d >= base*3/2 {
				t.Fatalf("attempt %d: wait %v, want %v ± 50 %%", n, d, base)
			}
		}
	}
}

func TestRulesCheck(t *testing.T) {
	if err := DefaultRules().Check(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Rules){
		"unknown rule": func(r *Rules) { r.Enabled["reboots"] = true },
		"percent":      func(r *Rules) { r.FullPercent = 20 },
		"per hour":     func(r *Rules) { r.PerHour = 0 },
		"quiet time":   func(r *Rules) { r.Quiet.From = "25:00" },
		"link":         func(r *Rules) { r.Link = "javascript:alert(1)" },
		"link user":    func(r *Rules) { r.Link = "https://user:pw@observarr.example" },
	} {
		r := DefaultRules()
		edit(&r)
		if r.Check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseRules([]byte(`{"enabled":{},"extra":1}`)); err == nil {
		t.Error("an unknown field was accepted")
	}
}
