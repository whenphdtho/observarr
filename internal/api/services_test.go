package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/config"
	"observarr/internal/poller"
	"observarr/internal/secret"
	"observarr/internal/shape"
	"observarr/internal/state"
	"observarr/internal/store"
	"observarr/internal/system"
)

// testKey is distinctive so that any leak into a response, a log line or the database is easy to find.
const testKey = "KEY-q7Zx3-never-show-me-9Vb"

// fakeApp is a test adapter. Its Detect asks the app server (newAppServer) for /status through the real
// adapter.GetJSON, so URLs, keys and network errors behave as with a real adapter.
type fakeApp struct {
	info    adapter.Info
	detects atomic.Int32
	probes  atomic.Int32
	prints  atomic.Int32 // Fingerprint calls

	mu       sync.Mutex
	probeKey string // the key of the latest probe
	probeURL string // the base URL of the latest probe
}

func (f *fakeApp) Info() adapter.Info { return f.info }

func (f *fakeApp) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	f.detects.Add(1)
	var status struct{ App, Version string }
	if err := adapter.GetJSON(ctx, t, "/status", "X-Api-Key", &status); err != nil {
		return adapter.Identity{}, err
	}
	if status.App != "fake" {
		return adapter.Identity{}, adapter.ErrWrongApp
	}
	return adapter.Identity{Version: status.Version}, nil
}

func (f *fakeApp) Probe(_ context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	f.probes.Add(1)
	f.mu.Lock()
	f.probeKey, f.probeURL = t.APIKey, t.BaseURL.String()
	f.mu.Unlock()
	return shape.Health{Version: "2.0", Warnings: []shape.Warning{}}, nil
}

// Fingerprint recognises the app server (newAppServer) without a key.
func (f *fakeApp) Fingerprint(ctx context.Context, t adapter.Target) (bool, string, error) {
	f.prints.Add(1)
	var v struct{ App string }
	if err := adapter.GetJSON(ctx, t, "/fingerprint", "", &v); err != nil {
		return false, "", err
	}
	return v.App == f.info.Type, "", nil
}

// LinkedApps lists what the app server's /linked (key required) names.
func (f *fakeApp) LinkedApps(ctx context.Context, t adapter.Target) ([]adapter.Candidate, error) {
	var items []struct{ Type, Name, URL string }
	if err := adapter.GetJSON(ctx, t, "/linked", "X-Api-Key", &items); err != nil {
		return nil, err
	}
	var out []adapter.Candidate
	for _, it := range items {
		u, err := url.Parse(it.URL)
		if err != nil {
			return nil, err
		}
		out = append(out, adapter.Candidate{Type: it.Type, Name: it.Name, BaseURL: u})
	}
	return out, nil
}

// ReadConfig recognises a folder holding "<type>.key", whose content is the key; the host is unknown.
func (f *fakeApp) ReadConfig(name string, dir fs.FS) (adapter.Candidate, bool, error) {
	key, err := fs.ReadFile(dir, f.info.Type+".key")
	if err != nil {
		return adapter.Candidate{}, false, nil
	}
	return adapter.Candidate{Type: f.info.Type, APIKey: string(key), KeySource: f.info.Type + ".key in " + name,
		BaseURL: &url.URL{Scheme: "http", Host: ":" + strconv.Itoa(f.info.DefaultPort), Path: "/"}}, true, nil
}

func (f *fakeApp) lastProbe() (key, url string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.probeKey, f.probeURL
}

// newAppServer serves a fake app at /status (key testKey required), the same app without a key at /open/status,
// and a different app at /other/status; /fingerprint names the app without a key, /linked (key required) lists
// a keyless app at /open and one of a type no adapter is registered for.
func newAppServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"app":"fake","version":"2.0"}`)
	})
	mux.HandleFunc("GET /open/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"app":"fake","version":"2.1"}`)
	})
	mux.HandleFunc("GET /other/status", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"app":"other","version":"9"}`)
	})
	mux.HandleFunc("GET /fingerprint", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"app":"fake"}`)
	})
	mux.HandleFunc("GET /linked", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != testKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `[{"type":"keyless","name":"Open","url":"http://%s/open"},{"type":"ghost","name":"G","url":"http://x"}]`,
			r.Host)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

type servicesEnv struct {
	*client
	app     *fakeApp // type "fake", needs a key
	keyless *fakeApp // type "keyless", needs none
	appURL  string
	appPort int // both fakes' DefaultPort, so a network scan of 127.0.0.1 finds the app server
	box     *secret.Box
}

// newServicesEnv starts a server with two fake adapters, a real store, box, hub and fast poller, and a
// logged-in client; options may change the server further. When the test ends it checks that testKey never
// reached a response, a log line or the data directory in plain text.
func newServicesEnv(t *testing.T, options ...func(*Server)) *servicesEnv {
	t.Helper()
	appURL := newAppServer(t).URL
	appPort, _ := strconv.Atoi(appURL[strings.LastIndex(appURL, ":")+1:])
	e := &servicesEnv{
		app: &fakeApp{info: adapter.Info{Type: "fake", Name: "Fake", DefaultPort: appPort, NeedsAPIKey: true,
			Kinds: []shape.Kind{shape.KindHealth}, Icon: "box"}},
		keyless: &fakeApp{info: adapter.Info{Type: "keyless", Name: "Keyless", DefaultPort: appPort, KeyLabel: "Token",
			Kinds: []shape.Kind{shape.KindHealth}}},
		appURL:  appURL,
		appPort: appPort,
	}
	reg := adapter.NewRegistry()
	reg.Register(e.app)
	reg.Register(e.keyless)
	box, err := secret.LoadOrCreate(filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	e.box = box
	ctx, cancel := context.WithCancel(context.Background())
	e.client = newServer(t, config.Config{}, append([]func(*Server){func(s *Server) {
		s.Registry, s.Box, s.Hub, s.HTTP, s.Context = reg, box, state.NewHub(), adapter.NewHTTPClient(), ctx
		s.Poller = poller.New(poller.Config{Registry: reg, Hub: s.Hub, HTTP: s.HTTP, Log: s.Log,
			Intervals: map[shape.Kind]time.Duration{shape.KindHealth: 20 * time.Millisecond}, Timeout: time.Second})
		s.pingEvery = 50 * time.Millisecond
		s.System = system.New(&fakeCollector{}, s.Hub.Watchers, s.Log)
	}}, options...)...)
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		if sampler, ok := e.srv.System.(*system.Sampler); ok {
			sampler.Run(ctx)
		}
	}()
	t.Cleanup(func() {
		e.srv.Poller.Close()
		cancel()
		<-sampled
		if strings.Contains(e.seen.String(), testKey) {
			t.Error("the API key appeared in a response body")
		}
		if strings.Contains(e.logs.String(), testKey) {
			t.Error("the API key appeared in the log")
		}
		files, _ := os.ReadDir(e.dir)
		for _, f := range files {
			if b, _ := os.ReadFile(filepath.Join(e.dir, f.Name())); strings.Contains(string(b), testKey) {
				t.Errorf("the API key is stored in plain text in %s", f.Name())
			}
		}
	})
	if code, _, _ := e.do("POST", "/api/setup", adminSetup, nil); code != 200 {
		t.Fatalf("setup = %d", code)
	}
	return e
}

// send makes a state-changing request with the CSRF token.
func (e *servicesEnv) send(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	code, data, _ := e.do(method, path, body, map[string]string{"X-CSRF-Token": e.csrf})
	return code, data
}

// create adds a fake service and returns its id.
func (e *servicesEnv) create(name string) int64 {
	e.t.Helper()
	code, data := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":%q,"url":%q,"apiKey":%q}`,
		name, e.appURL, testKey))
	if code != 201 {
		e.t.Fatalf("create %s = %d %v", name, code, data)
	}
	return int64(data["id"].(float64))
}

func (e *servicesEnv) path(id int64) string { return "/api/services/" + strconv.FormatInt(id, 10) }

// eventually fails the test unless cond becomes true within 2 s.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	eventuallyWithin(t, 2*time.Second, what, cond)
}

// eventuallyWithin fails the test unless cond becomes true within d.
func eventuallyWithin(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(d); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
	}
}

func TestAdaptersList(t *testing.T) {
	e := newServicesEnv(t)
	code, body, _ := e.raw("GET", "/api/adapters", "", nil)
	var infos []adapter.Info
	if err := json.Unmarshal(body, &infos); code != 200 || err != nil {
		t.Fatalf("adapters = %d %s", code, body)
	}
	if len(infos) != 2 || infos[0].Type != "fake" || !infos[0].NeedsAPIKey || infos[1].Type != "keyless" ||
		infos[0].DefaultPort != e.appPort || len(infos[0].Kinds) != 1 || infos[0].Icon != "box" || infos[1].Icon != "" {
		t.Fatalf("adapters = %+v", infos)
	}
}

func TestServiceTestEndpoint(t *testing.T) {
	e := newServicesEnv(t)
	body := func(typ, url, key string) string {
		return fmt.Sprintf(`{"type":%q,"url":%q,"apiKey":%q}`, typ, url, key)
	}
	if code, data := e.send("POST", "/api/services/test", body("fake", e.appURL+"/", testKey)); code != 200 || data["version"] != "2.0" {
		t.Fatalf("test = %d %v, want 200 version 2.0", code, data)
	}
	if code, data := e.send("POST", "/api/services/test", body("keyless", e.appURL+"/open", "")); code != 200 || data["version"] != "2.1" {
		t.Fatalf("keyless test = %d %v", code, data)
	}

	// The app answers, but Detect refuses: 422 with the adapter's message.
	for _, c := range []struct{ url, key, want string }{
		{e.appURL + "/other", testKey, adapter.ErrWrongApp.Error()},
		{e.appURL, testKey + "x", adapter.ErrUnauthorized.Error()},
		{e.appURL + "/missing", testKey, adapter.ErrNotFound.Error()},
	} {
		if code, data := e.send("POST", "/api/services/test", body("fake", c.url, c.key)); code != 422 || data["error"] != c.want {
			t.Errorf("test %s = %d %v, want 422 %q", c.url, code, data, c.want)
		}
	}

	// Nothing answers: 422 naming the host, with a short reason.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	code, data := e.send("POST", "/api/services/test", body("fake", "http://"+closed, testKey))
	if msg, _ := data["error"].(string); code != 422 || msg != "cannot reach "+closed+": connection refused" {
		t.Errorf("unreachable = %d %v", code, data)
	}

	// Invalid input never reaches the app: 400.
	long := strings.Repeat("k", 257)
	for _, b := range []string{
		body("nope", e.appURL, testKey),
		body("fake", "", testKey),
		body("fake", "ftp://"+closed, testKey),
		body("fake", "http://", testKey),
		body("fake", "http://:80", testKey),
		body("fake", "http://user:pass@"+closed, testKey),
		body("fake", "http://"+closed+"/?a=b", testKey),
		body("fake", "http://"+closed+"/?", testKey),
		body("fake", "http://"+closed+"/#x", testKey),
		body("fake", "not a url", testKey),
		body("fake", "http://"+closed, ""),
		body("fake", "http://"+closed, long),
		`{"type":"fake","url":"http://x","apiKey":"k","name":"not accepted here"}`,
	} {
		if code, data := e.send("POST", "/api/services/test", b); code != 400 {
			t.Errorf("test %s = %d %v, want 400", b, code, data)
		}
	}
	if n := e.app.detects.Load(); n != 5 {
		t.Errorf("Detect ran %d times, want 5 (invalid input must not reach the adapter)", n)
	}
}

func TestCreateService(t *testing.T) {
	e := newServicesEnv(t)
	code, data := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":"  My App  ","url":%q,"apiKey":%q}`,
		e.appURL+"//", testKey))
	if code != 201 || data["name"] != "My App" || data["type"] != "fake" || data["url"] != e.appURL {
		t.Fatalf("create = %d %v", code, data)
	}
	id := int64(data["id"].(float64))

	svc, err := e.srv.Store.ServiceByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if svc.BaseURL != e.appURL || svc.Name != "My App" {
		t.Fatalf("stored %+v", svc)
	}
	if key, err := e.box.Open(svc.SealedKey, "service:"+strconv.FormatInt(id, 10)); err != nil || string(key) != testKey {
		t.Fatalf("stored key does not open to the key: %v", err)
	}

	eventually(t, "the poller probes with the key", func() bool { key, _ := e.app.lastProbe(); return key == testKey })
	_, body, _ := e.raw("GET", "/api/services", "", nil)
	var list []state.ServiceState
	if err := json.Unmarshal(body, &list); err != nil || len(list) != 1 || list[0].ID != id || list[0].Name != "My App" {
		t.Fatalf("list = %s", body)
	}

	// Refused by the app: nothing is saved.
	if code, _ := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":"Other","url":%q,"apiKey":%q}`,
		e.appURL+"/other", testKey)); code != 422 {
		t.Fatalf("create of the wrong app = %d, want 422", code)
	}
	for _, name := range []string{"", "   ", strings.Repeat("n", 65)} {
		if code, _ := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":%q,"url":%q,"apiKey":%q}`,
			name, e.appURL, testKey)); code != 400 {
			t.Errorf("create with name %q = %d, want 400", name, code)
		}
	}
	if all, _ := e.srv.Store.Services(context.Background()); len(all) != 1 {
		t.Fatalf("%d services stored, want 1", len(all))
	}
}

func TestCreateKeylessService(t *testing.T) {
	e := newServicesEnv(t)
	code, data := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"keyless","name":"Open","url":%q}`, e.appURL+"/open"))
	if code != 201 {
		t.Fatalf("create = %d %v", code, data)
	}
	svc, err := e.srv.Store.ServiceByID(context.Background(), int64(data["id"].(float64)))
	if err != nil || svc.SealedKey != nil {
		t.Fatalf("keyless service stored with key %v (err %v), want none", svc.SealedKey, err)
	}
}

func TestDuplicateNameConflicts(t *testing.T) {
	e := newServicesEnv(t)
	e.create("Sonarr")
	second := e.create("Radarr")
	if code, _ := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":"SONARR","url":%q,"apiKey":%q}`,
		e.appURL, testKey)); code != 409 {
		t.Fatalf("duplicate create = %d, want 409", code)
	}
	if code, _ := e.send("PATCH", e.path(second), `{"name":"sonarr"}`); code != 409 {
		t.Fatalf("rename to a taken name = %d, want 409", code)
	}
}

func TestPatchNameOnlyKeepsKey(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("Before")
	before, _ := e.srv.Store.ServiceByID(context.Background(), id)
	detects := e.app.detects.Load()

	if code, data := e.send("PATCH", e.path(id), `{"name":"After"}`); code != 200 || data["name"] != "After" {
		t.Fatalf("patch = %d %v", code, data)
	}
	if n := e.app.detects.Load(); n != detects {
		t.Fatalf("a rename ran Detect (%d → %d)", detects, n)
	}
	after, _ := e.srv.Store.ServiceByID(context.Background(), id)
	if after.Name != "After" || after.BaseURL != before.BaseURL || string(after.SealedKey) != string(before.SealedKey) {
		t.Fatalf("after rename: %+v, before: %+v", after, before)
	}
	if snap := e.srv.Hub.Snapshot(); len(snap) != 1 || snap[0].Name != "After" {
		t.Fatalf("hub after rename: %+v", snap)
	}
	e.app.mu.Lock()
	e.app.probeKey = ""
	e.app.mu.Unlock()
	eventually(t, "the poller still probes with the key", func() bool { key, _ := e.app.lastProbe(); return key == testKey })
}

func TestPatchURLAndKey(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	moved := newAppServer(t).URL

	// The saved key never follows the service to another host on its own.
	if code, data := e.send("PATCH", e.path(id), fmt.Sprintf(`{"url":%q}`, moved)); code != 400 ||
		data["error"] != "enter the API key again when changing the address" {
		t.Fatalf("patch url without the key = %d %v, want 400", code, data)
	}
	detects := e.app.detects.Load()
	if code, data := e.send("PATCH", e.path(id), fmt.Sprintf(`{"url":%q,"apiKey":%q}`, moved+"/", testKey)); code != 200 || data["url"] != moved {
		t.Fatalf("patch url = %d %v", code, data)
	}
	if !(&servicesEnv{client: e.client}).hasAudit("changed service: App: " + e.appURL + " → " + moved) {
		t.Fatalf("audit = %v", e.auditRows())
	}
	if n := e.app.detects.Load(); n != detects+1 {
		t.Fatalf("a URL change ran Detect %d times, want once", n-detects)
	}
	eventually(t, "the poller uses the new URL", func() bool { _, url := e.app.lastProbe(); return url == moved })
	if svc, _ := e.srv.Store.ServiceByID(context.Background(), id); svc.BaseURL != moved {
		t.Fatalf("stored URL %q, want %q", svc.BaseURL, moved)
	}

	// Refused changes leave the service as it was.
	for body, want := range map[string]int{
		fmt.Sprintf(`{"url":%q}`, moved+"/other"):     422,
		fmt.Sprintf(`{"apiKey":%q}`, testKey+"wrong"): 422,
		`{"apiKey":""}`:      400, // this adapter needs a key
		`{"url":"ftp://x"}`:  400,
		`{"name":""}`:        400,
		`{"type":"keyless"}`: 400, // the type cannot change
	} {
		if code, data := e.send("PATCH", e.path(id), body); code != want {
			t.Errorf("patch %s = %d %v, want %d", body, code, data, want)
		}
	}
	if svc, _ := e.srv.Store.ServiceByID(context.Background(), id); svc.BaseURL != moved || svc.Name != "App" {
		t.Fatalf("after refused patches: %+v", svc)
	}
	if code, _ := e.send("PATCH", "/api/services/999", `{"name":"x"}`); code != 404 {
		t.Fatalf("patch of a missing service = %d, want 404", code)
	}
}

func TestPatchPathOnlyKeepsKey(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	before, _ := e.srv.Store.ServiceByID(context.Background(), id)
	if code, data := e.send("PATCH", e.path(id), fmt.Sprintf(`{"url":%q}`, e.appURL+"/open")); code != 200 {
		t.Fatalf("path-only change = %d %v", code, data)
	}
	after, _ := e.srv.Store.ServiceByID(context.Background(), id)
	if after.BaseURL != e.appURL+"/open" || string(after.SealedKey) != string(before.SealedKey) {
		t.Fatalf("after a path-only change: %+v", after)
	}
}

func TestSlowDetectDoesNotBlockOtherChanges(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	other := e.create("Other")
	arrived, release := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}
		<-release
		fmt.Fprint(w, `{"app":"fake","version":"2.0"}`)
	}))
	t.Cleanup(slow.Close)

	done := make(chan int)
	go func() {
		code, _ := e.send("PATCH", e.path(id), fmt.Sprintf(`{"url":%q,"apiKey":%q}`, slow.URL, testKey))
		done <- code
	}()
	<-arrived // the first change is waiting for Detect
	if code, _ := e.send("PATCH", e.path(other), `{"name":"Other renamed"}`); code != 200 {
		t.Fatalf("change of another service during a slow Detect = %d", code)
	}
	if code, _ := e.send("PATCH", e.path(id), `{"name":"Renamed"}`); code != 200 {
		t.Fatalf("rename during a slow Detect = %d", code)
	}
	close(release)
	if code := <-done; code != 409 {
		t.Fatalf("change of a service renamed meanwhile = %d, want 409", code)
	}
	if svc, _ := e.srv.Store.ServiceByID(context.Background(), id); svc.Name != "Renamed" || svc.BaseURL != e.appURL {
		t.Fatalf("after the refused change: %+v", svc)
	}
}

func TestMissingKeyIsShown(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	if _, err := e.srv.Store.ClearSealedKeys(context.Background()); err != nil {
		t.Fatal(err)
	}
	svc, _ := e.srv.Store.ServiceByID(context.Background(), id)
	e.srv.Poller.Stop(id)
	e.srv.startSaved(svc)
	if r := e.serviceState(id).Readings[shape.KindHealth]; r.Error != errKeyMissing.Error() {
		t.Fatalf("health of a service whose key was dropped = %+v", r)
	}
}

func TestPatchRemovesKey(t *testing.T) {
	e := newServicesEnv(t)
	code, data := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"keyless","name":"Open","url":%q,"apiKey":%q}`,
		e.appURL+"/open", testKey))
	if code != 201 || data["hasKey"] != true {
		t.Fatalf("create = %d %v", code, data)
	}
	id := int64(data["id"].(float64))
	// An optional key is also needed again for another address; the error names it as the adapter does.
	if code, data := e.send("PATCH", e.path(id), fmt.Sprintf(`{"url":%q}`, newAppServer(t).URL+"/open")); code != 400 ||
		data["error"] != "enter the token again when changing the address" {
		t.Fatalf("patch url without the key = %d %v, want 400", code, data)
	}
	detects := e.keyless.detects.Load()
	if code, data := e.send("PATCH", e.path(id), `{"apiKey":""}`); code != 200 || data["hasKey"] != false {
		t.Fatalf("remove key = %d %v", code, data)
	}
	if svc, _ := e.srv.Store.ServiceByID(context.Background(), id); svc.SealedKey != nil {
		t.Fatal("the key is still stored")
	}
	if e.keyless.detects.Load() != detects+1 {
		t.Fatal("a key change must re-run Detect")
	}
}

func TestPatchWithUndecryptableKey(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	svc, _ := e.srv.Store.ServiceByID(context.Background(), id)
	svc.SealedKey = []byte("garbage that cannot be opened")
	if err := e.srv.Store.UpdateService(context.Background(), svc); err != nil {
		t.Fatal(err)
	}
	if code, data := e.send("PATCH", e.path(id), `{"name":"Renamed"}`); code != 422 || data["error"] != errKeyUnreadable.Error() {
		t.Fatalf("patch without a new key = %d %v, want 422", code, data)
	}
	if code, data := e.send("PATCH", e.path(id), fmt.Sprintf(`{"apiKey":%q}`, testKey)); code != 200 {
		t.Fatalf("patch with the key entered again = %d %v", code, data)
	}
}

func TestDeleteStopsPoller(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	eventually(t, "the poller probes", func() bool { return e.app.probes.Load() > 0 })

	if code, _ := e.send("DELETE", e.path(id), ""); code != 204 {
		t.Fatalf("delete = %d, want 204", code)
	}
	probes := e.app.probes.Load()
	time.Sleep(100 * time.Millisecond) // five probe intervals
	if n := e.app.probes.Load(); n != probes {
		t.Fatalf("%d probes after delete, want none", n-probes)
	}
	if _, err := e.srv.Store.ServiceByID(context.Background(), id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("service still stored: %v", err)
	}
	if snap := e.srv.Hub.Snapshot(); len(snap) != 0 {
		t.Fatalf("hub still holds %+v", snap)
	}
	if code, _ := e.send("DELETE", e.path(id), ""); code != 404 {
		t.Fatalf("second delete = %d, want 404", code)
	}
	if code, _ := e.send("DELETE", "/api/services/abc", ""); code != 404 {
		t.Fatalf("delete of a non-numeric id = %d, want 404", code)
	}
}

func TestServiceRoutesNeedSessionAndCSRF(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	routes := []struct {
		method, path, body string
		write              bool
	}{
		{"GET", "/api/adapters", "", false},
		{"GET", "/api/services", "", false},
		{"GET", "/api/events", "", false},
		{"POST", "/api/services/test", fmt.Sprintf(`{"type":"fake","url":%q,"apiKey":%q}`, e.appURL, testKey), true},
		{"POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":"New","url":%q,"apiKey":%q}`, e.appURL, testKey), true},
		{"PATCH", e.path(id), `{"name":"Changed"}`, true},
		{"DELETE", e.path(id), "", true},
	}
	anon := newClientFor(e.client)
	for _, r := range routes {
		if code, _, _ := anon.raw(r.method, r.path, r.body, nil); code != 401 {
			t.Errorf("%s %s without a session = %d, want 401", r.method, r.path, code)
		}
		if !r.write {
			continue
		}
		if code, _, _ := e.raw(r.method, r.path, r.body, nil); code != 403 {
			t.Errorf("%s %s without CSRF = %d, want 403", r.method, r.path, code)
		}
	}
	if svc, err := e.srv.Store.ServiceByID(context.Background(), id); err != nil || svc.Name != "App" {
		t.Fatalf("a refused request changed the service: %+v %v", svc, err)
	}
}

func TestStartServices(t *testing.T) {
	e := newServicesEnv(t)
	ctx := context.Background()
	good, err := e.srv.Store.CreateService(ctx, store.Service{Type: "fake", Name: "Good", BaseURL: e.appURL},
		func(id int64) ([]byte, error) {
			return e.box.Seal([]byte(testKey), "service:"+strconv.FormatInt(id, 10))
		})
	if err != nil {
		t.Fatal(err)
	}
	// A key sealed for another row stands for any key that no longer opens (e.g. a replaced key file).
	moved, err := e.srv.Store.CreateService(ctx, store.Service{Type: "keyless", Name: "Moved", BaseURL: e.appURL + "/open"},
		func(id int64) ([]byte, error) {
			return e.box.Seal([]byte(testKey), "service:"+strconv.FormatInt(id+100, 10))
		})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.srv.StartServices(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the good service is up", func() bool {
		for _, s := range e.srv.Hub.Snapshot() {
			if s.ID == good.ID {
				return s.Up
			}
		}
		return false
	})
	time.Sleep(50 * time.Millisecond)
	if n := e.keyless.probes.Load(); n != 0 {
		t.Fatalf("the service with an unreadable key was probed %d times", n)
	}
	for _, s := range e.srv.Hub.Snapshot() {
		if s.ID != moved.ID {
			continue
		}
		if r := s.Readings[shape.KindHealth]; s.Up || r.Error != errKeyUnreadable.Error() {
			t.Fatalf("service with an unreadable key: %+v", s)
		}
		return
	}
	t.Fatal("the service with an unreadable key is not shown")
}

func TestInputBounds(t *testing.T) {
	e := newServicesEnv(t)
	body := func(url, key string) string {
		b, _ := json.Marshal(map[string]string{"type": "fake", "url": url, "apiKey": key})
		return string(b)
	}
	for _, c := range []struct{ body, want string }{
		{body(e.appURL, "key\r\nX-Injected: 1"), "apiKey: no control characters"},
		{body(e.appURL, "key\x00"), "apiKey: no control characters"},
		{body(e.appURL, "key\x7f"), "apiKey: no control characters"},
		{body(e.appURL+"/"+strings.Repeat("p", 2048), testKey), "url: at most 2048 bytes"},
	} {
		if code, data := e.send("POST", "/api/services/test", c.body); code != 400 || data["error"] != c.want {
			t.Errorf("test %.60s = %d %v, want 400 %q", c.body, code, data, c.want)
		}
	}
	if e.app.detects.Load() != 0 {
		t.Fatal("invalid input reached the adapter")
	}
	// A 2048-byte URL and a printable non-ASCII key pass validation (the app then refuses the key).
	longest := e.appURL + "/" + strings.Repeat("p", 2047-len(e.appURL))
	if code, data := e.send("POST", "/api/services/test", body(longest, "kéy")); code != 422 {
		t.Fatalf("valid input = %d %v, want 422 from the app", code, data)
	}
}
