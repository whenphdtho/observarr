package crowdsec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

const testKey = "test-bouncer-key"

var testNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sc is one expected breakdown row.
func sc(name string, n int) shape.SecurityCount { return shape.SecurityCount{Name: name, Count: n} }

func testAdapter() Adapter {
	return Adapter{now: func() time.Time { return testNow }, maxBytes: maxDecisionBytes, maxCount: maxDecisions}
}

// lapi serves the decisions endpoints like a LAPI would: 403 without the right key, body for /v1/decisions,
// and for a lookup of one address (?ip=) an empty answer.
func lapi(t *testing.T, body []byte) (adapter.Target, *[]string) {
	t.Helper()
	var paths []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.RequestURI())
		if r.Header.Get("X-Api-Key") != testKey {
			w.WriteHeader(http.StatusForbidden)
			w.Write(fixture(t, "forbidden.json"))
			return
		}
		if r.URL.Path != "/v1/decisions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("ip") != "" {
			w.Write([]byte("null"))
			return
		}
		w.Write(body)
	}))
	t.Cleanup(ts.Close)
	return target(t, ts.URL, testKey), &paths
}

func target(t *testing.T, raw, key string) adapter.Target {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return adapter.Target{BaseURL: u, APIKey: key, HTTP: &http.Client{}}
}

func TestInfo(t *testing.T) {
	info := New().Info()
	if info.Type != "crowdsec" || info.Icon != "shield" || !info.NeedsAPIKey ||
		!reflect.DeepEqual(info.Kinds, []shape.Kind{shape.KindHealth, shape.KindSecurity}) {
		t.Fatalf("Info = %+v", info)
	}
}

func TestDetect(t *testing.T) {
	tgt, paths := lapi(t, fixture(t, "decisions.json"))
	id, err := New().Detect(context.Background(), tgt)
	if err != nil || id.Version != "" {
		t.Fatalf("Detect = %+v, %v", id, err)
	}
	if want := []string{probePath}; !reflect.DeepEqual(*paths, want) {
		t.Fatalf("requests = %q, want %q", *paths, want)
	}
}

func TestDetectEmptyArray(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("[]")) }))
	defer ts.Close()
	if _, err := New().Detect(context.Background(), target(t, ts.URL, testKey)); err != nil {
		t.Fatalf("Detect on [] = %v", err)
	}
}

func TestDetectRejectedKey(t *testing.T) {
	tgt, _ := lapi(t, fixture(t, "decisions.json"))
	tgt.APIKey = "wrong"
	if _, err := New().Detect(context.Background(), tgt); !errors.Is(err, adapter.ErrUnauthorized) {
		t.Fatalf("Detect with a wrong key = %v, want ErrUnauthorized", err)
	}
}

func TestDetectWrongApp(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   []byte
	}{
		{"login page", 200, fixture(t, "not_crowdsec.html")},
		{"an object", 200, []byte(`{"appName":"Something","version":"1.0"}`)},
		{"no such endpoint", 404, []byte("not found")},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				w.Write(c.body)
			}))
			defer ts.Close()
			if _, err := New().Detect(context.Background(), target(t, ts.URL, testKey)); !errors.Is(err, adapter.ErrWrongApp) {
				t.Fatalf("Detect = %v, want ErrWrongApp", err)
			}
		})
	}
}

func TestProbeHealth(t *testing.T) {
	tgt, _ := lapi(t, fixture(t, "decisions.json"))
	got, err := New().Probe(context.Background(), tgt, shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	h, ok := got.(shape.Health)
	if !ok || h.Version != "" || h.Warnings == nil || len(h.Warnings) != 0 {
		t.Fatalf("health = %#v", got)
	}
}

func TestProbeUnknownKind(t *testing.T) {
	tgt, _ := lapi(t, fixture(t, "decisions.json"))
	if _, err := New().Probe(context.Background(), tgt, shape.KindQueue); !errors.Is(err, adapter.ErrUnknownKind) {
		t.Fatalf("Probe(queue) = %v, want ErrUnknownKind", err)
	}
}

func TestProbeSecurity(t *testing.T) {
	tgt, paths := lapi(t, fixture(t, "decisions.json"))
	got, err := testAdapter().Probe(context.Background(), tgt, shape.KindSecurity)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{decisionsPath}; !reflect.DeepEqual(*paths, want) {
		t.Fatalf("requests = %q, want %q", *paths, want)
	}
	s := got.(shape.Security)
	if s.Total != 8 || s.Local != 5 || s.Community != 3 || s.Capped {
		t.Fatalf("totals = %d/%d/%d capped %v, want 8/5/3", s.Total, s.Local, s.Community, s.Capped)
	}
	wantCounts := map[string][]shape.SecurityCount{
		"byType":   {sc("ban", 7), sc("captcha", 1)},
		"byOrigin": {sc("Detected here", 4), sc("Community blocklist", 2), sc("Added by hand", 1), sc("Subscribed lists", 1)},
		"byScope":  {sc("Ip", 7), sc("Range", 1)},
		"scenarios": {sc("crowdsecurity/ssh-bf", 2), sc("crowdsecurity/http-bad-user-agent", 1),
			sc("crowdsecurity/http-crawl-non_statics", 1), sc("manual 'ban' from 'observarr-docs'", 1)},
	}
	for name, got := range map[string][]shape.SecurityCount{"byType": s.ByType, "byOrigin": s.ByOrigin,
		"byScope": s.ByScope, "scenarios": s.TopScenarios} {
		if !reflect.DeepEqual(got, wantCounts[name]) {
			t.Errorf("%s = %v, want %v", name, got, wantCounts[name])
		}
	}
	var values []string
	for _, d := range s.Recent {
		values = append(values, d.Value)
	}
	if want := []string{"203.0.113.9", "203.0.113.46", "192.0.2.14", "192.0.2.0/28", "203.0.113.45"}; !reflect.DeepEqual(values, want) {
		t.Fatalf("recent = %q, want newest local first %q", values, want)
	}
	first, range4 := s.Recent[0], s.Recent[3]
	if !first.Until.Equal(time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)) || first.Type != "ban" ||
		first.Scenario != "crowdsecurity/http-bad-user-agent" || first.Origin != "Detected here" {
		t.Errorf("newest = %+v", first)
	}
	if !range4.Until.Equal(testNow.Add(71*time.Hour+2*time.Minute)) || range4.Scope != "Range" || range4.Origin != "Added by hand" {
		t.Errorf("range decision = %+v", range4)
	}
}

func TestProbeSecurityEmpty(t *testing.T) {
	for _, body := range []string{"null", "[]"} {
		tgt, _ := lapi(t, []byte(body))
		got, err := testAdapter().Probe(context.Background(), tgt, shape.KindSecurity)
		if err != nil {
			t.Fatal(err)
		}
		s := got.(shape.Security)
		if s.Total != 0 || s.ByType == nil || s.ByOrigin == nil || s.ByScope == nil || s.TopScenarios == nil || s.Recent == nil {
			t.Fatalf("%s: summary = %#v, want zero counts and empty (not nil) lists", body, s)
		}
	}
}

// decisionList builds a LAPI answer with community community decisions and local local ones, ids rising.
func decisionList(community, local int) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	id := 0
	write := func(origin, scenario, value string) {
		if id > 0 {
			b.WriteByte(',')
		}
		id++
		fmt.Fprintf(&b, `{"duration":"150h0m0s","id":%d,"origin":%q,"scenario":%q,"scope":"Ip","type":"ban","value":%q}`,
			id, origin, scenario, value)
	}
	for i := range community {
		write("CAPI", "crowdsecurity/ssh-bf", fmt.Sprintf("198.51.100.%d", i%256))
	}
	for i := range local {
		write("crowdsec", fmt.Sprintf("example/scenario-%d", i%400), fmt.Sprintf("203.0.113.%d", i%256))
	}
	b.WriteByte(']')
	return b.Bytes()
}

func TestProbeSecurityLargeList(t *testing.T) {
	tgt, _ := lapi(t, decisionList(60_000, 300))
	got, err := testAdapter().Probe(context.Background(), tgt, shape.KindSecurity)
	if err != nil {
		t.Fatal(err)
	}
	s := got.(shape.Security)
	if s.Total != 60_300 || s.Community != 60_000 || s.Local != 300 || s.Capped {
		t.Fatalf("totals = %d/%d/%d capped %v", s.Total, s.Community, s.Local, s.Capped)
	}
	if len(s.Recent) != shape.MaxSecurityDecisions || len(s.TopScenarios) != shape.MaxSecurityCounts {
		t.Fatalf("kept %d decisions and %d scenarios", len(s.Recent), len(s.TopScenarios))
	}
	if s.Recent[0].Value != "203.0.113.43" { // the last local decision: (300-1) % 256
		t.Fatalf("newest = %+v", s.Recent[0])
	}
}

func TestProbeSecurityCapped(t *testing.T) {
	body := decisionList(5000, 0)
	for name, a := range map[string]Adapter{
		"by count": {now: time.Now, maxBytes: maxDecisionBytes, maxCount: 1000},
		"by size":  {now: time.Now, maxBytes: int64(len(body) / 2), maxCount: maxDecisions},
	} {
		t.Run(name, func(t *testing.T) {
			tgt, _ := lapi(t, body)
			got, err := a.Probe(context.Background(), tgt, shape.KindSecurity)
			if err != nil {
				t.Fatal(err)
			}
			s := got.(shape.Security)
			if !s.Capped || s.Total == 0 || s.Total >= 5000 {
				t.Fatalf("total = %d capped %v, want a partial count marked capped", s.Total, s.Capped)
			}
		})
	}
}

func TestProbeSecurityClipsTexts(t *testing.T) {
	long := strings.Repeat("x", 10_000)
	body := fmt.Sprintf(`[{"id":1,"origin":"crowdsec","scenario":%q,"scope":"Ip","type":"ban","value":%q,"duration":"1h"}]`, long, long)
	tgt, _ := lapi(t, []byte(body))
	got, err := testAdapter().Probe(context.Background(), tgt, shape.KindSecurity)
	if err != nil {
		t.Fatal(err)
	}
	s := got.(shape.Security)
	if len(s.Recent[0].Value) > maxNameBytes || len(s.Recent[0].Scenario) > maxNameBytes || len(s.TopScenarios[0].Name) > maxNameBytes {
		t.Fatalf("texts not clipped: %d, %d bytes", len(s.Recent[0].Value), len(s.Recent[0].Scenario))
	}
}

func TestCounterBoundsNames(t *testing.T) {
	c := counter{}
	for i := range maxNames + 100 {
		c.add(fmt.Sprint("name-", i))
	}
	if len(c) != maxNames+1 || c[otherName] != 100 {
		t.Fatalf("%d names, %d other", len(c), c[otherName])
	}
}

func TestProbeSecurityErrors(t *testing.T) {
	tgt, _ := lapi(t, []byte(`{"message":"oops"}`))
	if _, err := testAdapter().Probe(context.Background(), tgt, shape.KindSecurity); !errors.Is(err, adapter.ErrNotJSON) {
		t.Fatalf("object answer = %v, want ErrNotJSON", err)
	}
	tgt.APIKey = "wrong"
	if _, err := testAdapter().Probe(context.Background(), tgt, shape.KindSecurity); !errors.Is(err, adapter.ErrUnauthorized) {
		t.Fatalf("wrong key = %v, want ErrUnauthorized", err)
	}
}
