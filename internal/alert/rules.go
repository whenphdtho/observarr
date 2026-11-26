// Package alert tells the user when something needs attention, even when nobody has Observarr open: it turns
// the live state into conditions (conditions.go), decides when a condition is worth a message (engine.go), and
// delivers messages to the user's channels (channels.go, deliver.go).
package alert

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Rule names a group of conditions the user can switch on or off, globally and per channel.
type Rule string

const (
	RuleDown      Rule = "down"      // a service is down, or a container dead, restarting or unhealthy
	RuleHealth    Rule = "health"    // a service reports a health warning or error
	RuleStorage   Rule = "storage"   // a disk or cache is nearly full, or a mount is unhealthy
	RuleTraffic   Rule = "traffic"   // the traffic budget is 80 % or 100 % used
	RuleSecurity  Rule = "security"  // sign-ins are locked out or limited, or many failed
	RuleDownloads Rule = "downloads" // a download is stuck or failed
	RuleRequests  Rule = "requests"  // media requests wait for approval
)

// AllRules lists every rule, in the order the UI shows them.
var AllRules = []Rule{RuleDown, RuleHealth, RuleStorage, RuleTraffic, RuleSecurity, RuleDownloads, RuleRequests}

// Severity ranks an alert. Critical alerts go out even in quiet hours.
type Severity string

const (
	Critical Severity = "critical"
	Warning  Severity = "warning"
	Info     Severity = "info"
)

// Rules is what raises alerts and how often they go out. It is saved as one JSON document.
type Rules struct {
	Enabled       map[Rule]bool `json:"enabled"`
	DownMinutes   int           `json:"downMinutes"`   // a service must be down this long before it is reported
	FullPercent   int           `json:"fullPercent"`   // a disk or cache this full is nearly full
	FailedSignIns int           `json:"failedSignIns"` // this many failed sign-ins within 15 minutes are many
	RepeatHours   int           `json:"repeatHours"`   // a still-firing alert is sent again after this long; 0 = never
	PerHour       int           `json:"perHour"`       // at most this many messages per channel and hour
	Quiet         Quiet         `json:"quiet"`
	// Link is Observarr's own address, for links in alerts; "" leaves them out.
	Link string `json:"link"`
}

// Quiet hours hold every alert but critical ones from From until To (local time, "HH:MM"); they are summed up in
// one message afterwards.
type Quiet struct {
	On   bool   `json:"on"`
	From string `json:"from"`
	To   string `json:"to"`
}

// DefaultRules are the rules before the user changes any.
func DefaultRules() Rules {
	return Rules{
		Enabled: map[Rule]bool{RuleDown: true, RuleHealth: true, RuleStorage: true, RuleTraffic: true,
			RuleSecurity: true, RuleDownloads: true, RuleRequests: false},
		DownMinutes: 3, FullPercent: 90, FailedSignIns: 10, RepeatHours: 0, PerHour: 30,
		Quiet: Quiet{From: "22:00", To: "07:00"},
	}
}

// ParseRules reads a rules document strictly and checks it. Rules missing from Enabled are off.
func ParseRules(body []byte) (Rules, error) {
	var r Rules
	if err := decodeStrict(body, &r); err != nil {
		return Rules{}, err
	}
	return r, r.Check()
}

// Check reports the first thing wrong with r.
func (r Rules) Check() error {
	for rule := range r.Enabled {
		if !slices.Contains(AllRules, rule) {
			return fmt.Errorf("enabled: unknown rule %q", rule)
		}
	}
	for _, f := range []struct {
		name      string
		v, lo, hi int
	}{{"downMinutes", r.DownMinutes, 0, 1440}, {"fullPercent", r.FullPercent, 50, 100},
		{"failedSignIns", r.FailedSignIns, 3, 1000}, {"repeatHours", r.RepeatHours, 0, 168},
		{"perHour", r.PerHour, 1, 600}} {
		if f.v < f.lo || f.v > f.hi {
			return fmt.Errorf("%s: %d to %d", f.name, f.lo, f.hi)
		}
	}
	if _, err := clock(r.Quiet.From); err != nil {
		return fmt.Errorf("quiet.from: %w", err)
	}
	if _, err := clock(r.Quiet.To); err != nil {
		return fmt.Errorf("quiet.to: %w", err)
	}
	if r.Quiet.From == r.Quiet.To {
		return errors.New("quiet: from and to must differ")
	}
	if r.Link != "" {
		if _, err := checkURL(r.Link, false); err != nil {
			return fmt.Errorf("link: %w", err)
		}
	}
	return nil
}

// on reports whether rule is switched on.
func (r Rules) on(rule Rule) bool { return r.Enabled[rule] }

// everyRule is r with every rule switched on.
func (r Rules) everyRule() Rules {
	r.Enabled = make(map[Rule]bool, len(AllRules))
	for _, rule := range AllRules {
		r.Enabled[rule] = true
	}
	return r
}

// debounce is how long a condition of rule must last before it is reported: long enough that a blip is not.
func (r Rules) debounce(rule Rule) time.Duration {
	switch rule {
	case RuleDown:
		return time.Duration(r.DownMinutes) * time.Minute
	case RuleStorage, RuleDownloads, RuleRequests:
		return 5 * time.Minute
	case RuleHealth:
		return time.Minute
	}
	return 0 // traffic and security: their figures do not flap
}

// quietAt reports whether t falls in the quiet hours.
func (r Rules) quietAt(t time.Time) bool {
	if !r.Quiet.On {
		return false
	}
	from, _ := clock(r.Quiet.From)
	to, _ := clock(r.Quiet.To)
	now := t.Hour()*60 + t.Minute()
	if from <= to {
		return now >= from && now < to
	}
	return now >= from || now < to // across midnight
}

// clock parses "HH:MM" into minutes after midnight.
func clock(s string) (int, error) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, errors.New(`a time as "HH:MM"`)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// checkURL checks a destination address as a service address is checked: http or https, a host, no user name or
// fragment, at most 2048 bytes. A query is allowed only with query set: some webhooks carry their token in it.
func checkURL(s string, query bool) (*url.URL, error) {
	u, err := url.Parse(s)
	switch {
	case len(s) > 2048:
		return nil, errors.New("at most 2048 bytes")
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "":
		return nil, errors.New("an http:// or https:// address")
	case u.User != nil:
		return nil, errors.New("no user name or password in the address")
	case strings.Contains(s, "#") || (!query && strings.Contains(s, "?")):
		if query {
			return nil, errors.New("no fragment in the address")
		}
		return nil, errors.New("no query or fragment in the address")
	}
	return u, nil
}

// decodeStrict decodes exactly one JSON value into v, refusing unknown fields.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("data after the JSON value")
	}
	return nil
}
