// Package crowdsec implements the Observarr adapter for CrowdSec's Local API (LAPI). It reads, with a bouncer API
// key, the decisions the LAPI hands its bouncers, and nothing else: alerts need machine credentials, which this
// adapter neither asks for nor uses.
package crowdsec

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// apiKeyHeader is the header the LAPI expects a bouncer's key on.
const apiKeyHeader = "X-Api-Key"

// probePath asks for the decisions against one address, which is a quick call however many decisions exist: it
// confirms the LAPI and the key without pulling every decision. 127.0.0.1 is never banned in practice; any answer
// is fine.
const probePath = "/v1/decisions?ip=127.0.0.1"

// decisionsPath lists every active decision.
const decisionsPath = "/v1/decisions"

const (
	// maxDecisions bounds how many decisions are counted; beyond it the summary says it is capped.
	maxDecisions = 500_000
	// maxDecisionBytes bounds the answer listing every decision. Subscribed blocklists can hold tens of
	// thousands of addresses, about 200 bytes each; decisions are counted as they are read, never kept.
	maxDecisionBytes = 48 << 20
	// maxNames bounds the distinct names one breakdown tracks; the rest are counted under otherName.
	maxNames  = 256
	otherName = "other"
	// maxNameBytes bounds one name (an origin, a scenario) as a breakdown key.
	maxNameBytes = 128
)

// Adapter is the CrowdSec LAPI adapter.
type Adapter struct {
	now      func() time.Time
	maxBytes int64 // bounds the answer listing every decision
	maxCount int   // bounds the decisions counted
}

// New returns the CrowdSec adapter.
func New() adapter.Adapter {
	return Adapter{now: time.Now, maxBytes: maxDecisionBytes, maxCount: maxDecisions}
}

// Info describes CrowdSec for the "Add apps" page.
func (Adapter) Info() adapter.Info {
	return adapter.Info{
		Type:        "crowdsec",
		Name:        "CrowdSec",
		DefaultPort: 8080,
		NeedsAPIKey: true,
		Kinds:       []shape.Kind{shape.KindHealth, shape.KindSecurity},
		Icon:        "shield",
	}
}

// decision is one entry of GET /v1/decisions.
type decision struct {
	ID       int64  `json:"id"`
	Origin   string `json:"origin"`
	Scenario string `json:"scenario"`
	Scope    string `json:"scope"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	Duration string `json:"duration"` // time left, e.g. "3h59m12.5s"
	Until    string `json:"until"`    // end time; not every version sends it
}

// lookup asks the LAPI about one address and reports how long that took. Any decision list (null or an array) is
// a valid answer.
func lookup(ctx context.Context, t adapter.Target) (time.Duration, error) {
	start := time.Now()
	err := adapter.StreamJSON(ctx, t, probePath, apiKeyHeader, func(d *json.Decoder) error {
		_, err := adapter.DecodeArray(d, shape.MaxSecurityDecisions, func(decision) {})
		return err
	})
	return time.Since(start), err
}

// Detect confirms a CrowdSec LAPI that accepts the key. The LAPI does not tell a bouncer its version, so the
// version stays empty.
func (Adapter) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	if _, err := lookup(ctx, t); err != nil {
		// No such endpoint, or something that is not a decision list (a login page, another app's JSON).
		if errors.Is(err, adapter.ErrNotJSON) || errors.Is(err, adapter.ErrNotFound) {
			return adapter.Identity{}, adapter.ErrWrongApp
		}
		return adapter.Identity{}, err
	}
	return adapter.Identity{}, nil
}

// Probe fills one declared shape kind.
func (a Adapter) Probe(ctx context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	switch kind {
	case shape.KindHealth:
		latency, err := lookup(ctx, t)
		if err != nil {
			return nil, err
		}
		return shape.Health{LatencyMS: latency.Milliseconds(), Warnings: []shape.Warning{}}, nil
	case shape.KindSecurity:
		return a.probeSecurity(ctx, t)
	default:
		return nil, adapter.ErrUnknownKind
	}
}

// probeSecurity reads every active decision once, counting each and keeping only the newest local ones.
func (a Adapter) probeSecurity(ctx context.Context, t adapter.Target) (any, error) {
	now := a.now()
	s := newSummary()
	var capped bool
	err := adapter.StreamJSONLimit(ctx, t, decisionsPath, apiKeyHeader, a.maxBytes, func(d *json.Decoder) error {
		var err error
		capped, err = adapter.DecodeArray(d, a.maxCount, s.add)
		return err
	})
	switch {
	case errors.Is(err, adapter.ErrTooLarge):
		capped = true // what was counted before the limit still stands
	case err != nil:
		return nil, err
	}
	return s.result(now, capped), nil
}

// community reports whether origin stands for a shared blocklist rather than this machine: the central API's
// community blocklist, or a subscribed list.
func community(origin string) bool {
	o := strings.ToLower(origin)
	return o == "capi" || o == "lists" || strings.HasPrefix(o, "lists:")
}

// originLabel says in plain words where a decision came from; an origin it does not know is given as it is.
func originLabel(origin string) string {
	switch o := strings.ToLower(strings.TrimSpace(origin)); {
	case o == "capi":
		return "Community blocklist"
	case o == "lists" || strings.HasPrefix(o, "lists:"):
		return "Subscribed lists"
	case o == "cscli":
		return "Added by hand"
	case o == "crowdsec":
		return "Detected here"
	}
	return origin
}

// counter counts names, tracking at most maxNames distinct ones.
type counter map[string]int

func (c counter) add(name string) {
	name = shape.ClipTo(strings.TrimSpace(name), maxNameBytes)
	if name == "" {
		name = "unknown"
	}
	if _, ok := c[name]; !ok && len(c) >= maxNames {
		name = otherName
	}
	c[name]++
}

// top returns c's largest counts, at most shape.MaxSecurityCounts, largest first and then by name.
func (c counter) top() []shape.SecurityCount {
	out := make([]shape.SecurityCount, 0, len(c))
	for name, n := range c {
		out = append(out, shape.SecurityCount{Name: name, Count: n})
	}
	slices.SortFunc(out, func(a, b shape.SecurityCount) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out[:min(len(out), shape.MaxSecurityCounts)]
}

// summary gathers the counts while the decisions are read.
type summary struct {
	total, local, community int
	types, origins, scopes  counter
	scenarios               counter
	recent                  []decision // the newest local decisions, at most shape.MaxSecurityDecisions
}

func newSummary() *summary {
	return &summary{types: counter{}, origins: counter{}, scopes: counter{}, scenarios: counter{}}
}

func (s *summary) add(d decision) {
	s.total++
	s.types.add(strings.ToLower(d.Type))
	s.origins.add(originLabel(d.Origin))
	s.scopes.add(d.Scope)
	if community(d.Origin) {
		s.community++
		return
	}
	s.local++
	s.scenarios.add(d.Scenario)
	// Keep the newest: the LAPI numbers decisions in the order they were made.
	if len(s.recent) < shape.MaxSecurityDecisions {
		s.recent = append(s.recent, clipped(d))
		return
	}
	oldest := 0
	for i, r := range s.recent {
		if r.ID < s.recent[oldest].ID {
			oldest = i
		}
	}
	if d.ID > s.recent[oldest].ID {
		s.recent[oldest] = clipped(d)
	}
}

// clipped bounds every text of d, so the decisions kept stay small however long the app's texts are.
func clipped(d decision) decision {
	for _, p := range []*string{&d.Origin, &d.Scenario, &d.Scope, &d.Type, &d.Value, &d.Duration, &d.Until} {
		*p = shape.ClipTo(*p, maxNameBytes)
	}
	return d
}

func (s *summary) result(now time.Time, capped bool) shape.Security {
	slices.SortFunc(s.recent, func(a, b decision) int { return cmp.Compare(b.ID, a.ID) })
	recent := make([]shape.SecurityDecision, 0, len(s.recent))
	for _, d := range s.recent {
		recent = append(recent, shape.SecurityDecision{
			Value:    d.Value,
			Scope:    d.Scope,
			Type:     strings.ToLower(d.Type),
			Origin:   originLabel(d.Origin),
			Scenario: d.Scenario,
			Until:    until(d, now),
		})
	}
	return shape.Security{
		Total:        s.total,
		Local:        s.local,
		Community:    s.community,
		Capped:       capped,
		ByType:       s.types.top(),
		ByOrigin:     s.origins.top(),
		ByScope:      s.scopes.top(),
		TopScenarios: s.scenarios.top(),
		Recent:       recent,
	}
}

// until is when d ends: its own end time if it has one, else now plus the time it has left, to the second. It is
// zero when neither is usable.
func until(d decision, now time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, d.Until); err == nil && !t.IsZero() {
		return t.UTC().Truncate(time.Second)
	}
	left, err := time.ParseDuration(d.Duration)
	if err != nil || left <= 0 {
		return time.Time{}
	}
	return now.Add(left).UTC().Round(time.Minute)
}
