package seerr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

const testKey = "test-api-key"

// fake is an app sharing Overseerr's API. Its fields are the answers; a path missing from extra is 404.
type fake struct {
	mu       sync.Mutex
	status   string
	public   string
	appData  string
	failed   int // status of request?filter=failed, if not 200
	issues   int // status of issue/count, if not 200
	titleErr bool
	paths    []string
}

func newFake() *fake {
	return &fake{
		status:  `{"version":"1.33.2","commitTag":"v1.33.2","updateAvailable":false,"commitsBehind":0,"restartRequired":false}`,
		public:  `{"initialized":true,"applicationTitle":"Requests","localLogin":true}`,
		appData: `{"appData":true,"appDataPath":"/app/config"}`,
	}
}

func (f *fake) serve(t *testing.T) adapter.Target {
	t.Helper()
	requests, err := os.ReadFile(filepath.Join("testdata", "requests.json"))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.paths = append(f.paths, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/status":
			w.Write([]byte(f.status))
			return
		case "/api/v1/status/appdata":
			w.Write([]byte(f.appData))
			return
		case "/api/v1/settings/public":
			w.Write([]byte(f.public))
			return
		}
		if r.Header.Get("X-Api-Key") != testKey {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"status":403,"error":"You do not have permission to access this endpoint"}`))
			return
		}
		q := r.URL.Query()
		switch {
		case r.URL.Path == "/api/v1/request/count":
			w.Write([]byte(`{"total":9,"movie":5,"tv":4,"pending":2,"approved":5,"declined":1,"processing":1,"available":3}`))
		case r.URL.Path == "/api/v1/request" && q.Get("filter") == "failed":
			if f.failed != 0 {
				w.WriteHeader(f.failed)
				w.Write([]byte(`{"message":"bad request"}`))
				return
			}
			w.Write([]byte(`{"pageInfo":{"pages":1,"pageSize":1,"results":1,"page":1},"results":[]}`))
		case r.URL.Path == "/api/v1/request" && q.Get("filter") == "all" && q.Get("take") == "10":
			w.Write(requests)
		case r.URL.Path == "/api/v1/issue/count":
			if f.issues != 0 {
				w.WriteHeader(f.issues)
				return
			}
			w.Write([]byte(`{"total":5,"video":2,"audio":1,"subtitles":1,"others":1,"open":3,"closed":2}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/movie/") || strings.HasPrefix(r.URL.Path, "/api/v1/tv/"):
			if f.titleErr {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			title, ok := map[string]string{
				"/api/v1/movie/45745":  `{"id":45745,"title":"Sintel","credits":{"cast":[{"name":"Halina Reijn"}]}}`,
				"/api/v1/tv/90210":     `{"id":90210,"name":"Pepper & Carrot"}`,
				"/api/v1/movie/133701": `{"id":133701,"title":"Tears of Steel"}`,
				"/api/v1/movie/358332": `{"id":358332,"title":"Cosmos Laundromat"}`,
				"/api/v1/tv/90211":     `{"id":90211,"name":"Caminandes"}`,
				"/api/v1/movie/10378":  `{"id":10378,"title":"Big Buck Bunny"}`,
			}[r.URL.Path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(title))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	return adapter.Target{BaseURL: u, APIKey: testKey, HTTP: &http.Client{}}
}

func (f *fake) requested() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.paths...)
}

func TestInfo(t *testing.T) {
	info := New().Info()
	if info.Type != "seerr" || info.DefaultPort != 5055 || !info.NeedsAPIKey ||
		!reflect.DeepEqual(info.Kinds, []shape.Kind{shape.KindHealth, shape.KindRequests}) {
		t.Fatalf("Info = %+v", info)
	}
}

func TestLogo(t *testing.T) {
	if err := adapter.CheckLogo(New().(adapter.Branded).Logo()); err != nil {
		t.Fatal(err)
	}
}

func TestDetect(t *testing.T) {
	f := newFake()
	id, err := New().Detect(context.Background(), f.serve(t))
	if err != nil || id.Version != "1.33.2" {
		t.Fatalf("Detect = %+v, %v", id, err)
	}
}

func TestDetectRejectedKey(t *testing.T) {
	tgt := newFake().serve(t)
	tgt.APIKey = "wrong"
	if _, err := New().Detect(context.Background(), tgt); !errors.Is(err, adapter.ErrUnauthorized) {
		t.Fatalf("Detect with a wrong key = %v, want ErrUnauthorized", err)
	}
}

func TestDetectWrongApp(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
	}{
		{"html page", 200, "<!doctype html><title>Something</title>"},
		{"other json", 200, `{"version":"10.9.0","productName":"Something"}`},
		{"no version", 200, `{"version":"","commitTag":"x","restartRequired":false}`},
		{"no such endpoint", 404, "not found"},
		{"method not allowed", 405, "no"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				w.Write([]byte(c.body))
			}))
			defer ts.Close()
			u, _ := url.Parse(ts.URL)
			tgt := adapter.Target{BaseURL: u, APIKey: testKey, HTTP: &http.Client{}}
			if _, err := New().Detect(context.Background(), tgt); !errors.Is(err, adapter.ErrWrongApp) {
				t.Fatalf("Detect = %v, want ErrWrongApp", err)
			}
			if ok, _, err := New().(adapter.Fingerprinter).Fingerprint(context.Background(), tgt); ok || err != nil {
				t.Fatalf("Fingerprint = %v, %v; want false, nil", ok, err)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	tgt := newFake().serve(t)
	tgt.APIKey = ""
	ok, version, err := New().(adapter.Fingerprinter).Fingerprint(context.Background(), tgt)
	if !ok || version != "1.33.2" || err != nil {
		t.Fatalf("Fingerprint = %v, %q, %v", ok, version, err)
	}

	ts := httptest.NewServer(http.NotFoundHandler())
	u, _ := url.Parse(ts.URL)
	ts.Close()
	if ok, _, err := New().(adapter.Fingerprinter).Fingerprint(context.Background(),
		adapter.Target{BaseURL: u, HTTP: &http.Client{}}); ok || err == nil {
		t.Fatalf("Fingerprint of a closed port = %v, %v; want an error", ok, err)
	}
}

func TestMatchesImage(t *testing.T) {
	a := New().(adapter.ImageMatcher)
	for _, img := range []string{"sctx/overseerr:latest", "lscr.io/linuxserver/overseerr", "hotio/overseerr:release",
		"fallenbagel/jellyseerr:2.7.3", "ghcr.io/seerr-team/seerr:v3"} {
		if !a.MatchesImage(img) {
			t.Errorf("MatchesImage(%q) = false", img)
		}
	}
	for _, img := range []string{"lscr.io/linuxserver/sonarr", "jellyfin/jellyfin", "overseerr-proxy"} {
		if a.MatchesImage(img) {
			t.Errorf("MatchesImage(%q) = true", img)
		}
	}
}

func TestProbeHealth(t *testing.T) {
	f := newFake()
	f.status = `{"version":"1.33.2","commitTag":"v1.33.2","updateAvailable":true,"commitsBehind":4,"restartRequired":true}`
	f.appData = `{"appData":false,"appDataPath":"/app/config"}`
	got, err := New().Probe(context.Background(), f.serve(t), shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	h := got.(shape.Health)
	want := []shape.Warning{
		{Level: shape.LevelWarning, Message: "a restart is needed to apply changed settings"},
		{Level: shape.LevelInfo, Message: "an update is available"},
		{Level: shape.LevelWarning, Message: "the settings are not on a mounted volume: recreating the container loses them"},
		{Level: shape.LevelInfo, Message: "2 requests waiting for approval"},
		{Level: shape.LevelWarning, Message: "1 request failed"},
	}
	if h.Version != "1.33.2" || !reflect.DeepEqual(h.Warnings, want) {
		t.Fatalf("Health = %+v", h)
	}
}

func TestProbeHealthRejectedKeyIsAWarning(t *testing.T) {
	tgt := newFake().serve(t)
	tgt.APIKey = "wrong"
	got, err := New().Probe(context.Background(), tgt, shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	want := []shape.Warning{{Level: shape.LevelWarning,
		Message: "the requests cannot be read: the app rejected the API key"}}
	if h := got.(shape.Health); !reflect.DeepEqual(h.Warnings, want) {
		t.Fatalf("Warnings = %+v", h.Warnings)
	}
}

func TestProbeRequests(t *testing.T) {
	f := newFake()
	tgt := f.serve(t)
	got, err := New().Probe(context.Background(), tgt, shape.KindRequests)
	if err != nil {
		t.Fatal(err)
	}
	r := got.(shape.Requests)
	at := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	three := 3
	want := shape.Requests{
		App:    "Overseerr",
		Counts: shape.RequestCounts{Total: 9, Pending: 2, Approved: 5, Processing: 1, Available: 3, Declined: 1, Failed: 1},
		Latest: []shape.MediaRequest{
			{Title: "Sintel", Type: "movie", RequestedBy: "alex", State: shape.RequestPending, At: at("2026-11-23T10:30:00Z")},
			{Title: "Pepper & Carrot", Type: "series", RequestedBy: "sam", State: shape.RequestProcessing, At: at("2026-11-22T18:05:00Z")},
			{Title: "Tears of Steel", Type: "movie", RequestedBy: "", State: shape.RequestProcessing, At: at("2026-11-21T08:00:00Z")},
			{Title: "Cosmos Laundromat", Type: "movie", RequestedBy: "alex", State: shape.RequestAvailable, At: at("2026-11-20T08:00:00Z")},
			{Title: "Caminandes", Type: "series", RequestedBy: "sam", State: shape.RequestPartial, At: at("2026-11-19T08:00:00Z")},
			{Title: "Big Buck Bunny", Type: "movie", RequestedBy: "robin", State: shape.RequestDeclined, At: at("2026-11-18T08:00:00Z")},
			{Title: "Unknown title", Type: "movie", RequestedBy: "robin", State: shape.RequestFailed},
		},
		OpenIssues: &three,
	}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("Requests =\n%+v\nwant\n%+v", r, want)
	}

}

func TestProbeRequestsRemembersTitles(t *testing.T) {
	f := newFake()
	tgt := f.serve(t)
	a := New()
	if _, err := a.Probe(context.Background(), tgt, shape.KindRequests); err != nil {
		t.Fatal(err)
	}
	mark := len(f.requested())
	if _, err := a.Probe(context.Background(), tgt, shape.KindRequests); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.requested()[mark:] {
		if strings.Contains(p, "/movie/") || strings.Contains(p, "/tv/") {
			t.Errorf("looked up %s again", p)
		}
	}
}

func TestProbeRequestsNeverPassesAnEmail(t *testing.T) {
	got, err := New().Probe(context.Background(), newFake().serve(t), shape.KindRequests)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got.(shape.Requests).Latest {
		if strings.Contains(r.RequestedBy, "@") {
			t.Errorf("requester %q is an e-mail address", r.RequestedBy)
		}
	}
}

func TestProbeRequestsApp(t *testing.T) {
	for _, c := range []struct{ status, public, want string }{
		{`{"version":"2.7.3","commitTag":"v2.7.3","restartRequired":false}`,
			`{"initialized":true,"applicationTitle":"Jellyseerr","mediaServerType":2}`, "Jellyseerr"},
		{`{"version":"3.0.1","commitTag":"v3.0.1","restartRequired":false}`,
			`{"initialized":true,"mediaServerType":1}`, "Seerr"},
		{`{"version":"1.34.0","commitTag":"v1.34.0","restartRequired":false}`,
			`{"initialized":true,"applicationTitle":"Jellyseerr"}`, "Overseerr"},
	} {
		f := newFake()
		f.status, f.public = c.status, c.public
		got, err := New().Probe(context.Background(), f.serve(t), shape.KindRequests)
		if err != nil {
			t.Fatal(err)
		}
		if app := got.(shape.Requests).App; app != c.want {
			t.Errorf("App = %q, want %q", app, c.want)
		}
	}
}

func TestProbeRequestsOlderVersions(t *testing.T) {
	f := newFake()
	f.failed, f.issues, f.titleErr = http.StatusBadRequest, http.StatusNotFound, true
	got, err := New().Probe(context.Background(), f.serve(t), shape.KindRequests)
	if err != nil {
		t.Fatal(err)
	}
	r := got.(shape.Requests)
	if r.Counts.Failed != 0 || r.OpenIssues != nil || r.Latest[0].Title != "TMDB 45745" {
		t.Fatalf("Requests = %+v", r)
	}
}

func TestProbeUnknownKind(t *testing.T) {
	if _, err := New().Probe(context.Background(), newFake().serve(t), shape.KindQueue); !errors.Is(err, adapter.ErrUnknownKind) {
		t.Fatalf("Probe(queue) = %v", err)
	}
}

func TestTitleCacheIsBounded(t *testing.T) {
	c := &titleCache{m: map[string]string{}}
	for i := range maxTitles + 5 {
		c.put(strings.Repeat("k", i+1), "t")
	}
	if len(c.m) > maxTitles {
		t.Fatalf("cache holds %d titles", len(c.m))
	}
}
