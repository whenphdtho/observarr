package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/collect"
	"observarr/internal/discovery"
	"observarr/internal/store"
)

// discoveryRun starts a discovery run and decodes its response.
func (e *servicesEnv) discoveryRun(path, body string) (int, discovery.Result) {
	e.t.Helper()
	header := map[string]string{"X-CSRF-Token": e.csrf}
	code, raw, _ := e.raw("POST", path, body, header)
	var res discovery.Result
	if code == 200 {
		if err := json.Unmarshal(raw, &res); err != nil {
			e.t.Fatalf("%s: %s", path, raw)
		}
	}
	return code, res
}

// auditRows returns "action: detail" for every audit row.
func (e *servicesEnv) auditRows() []string {
	e.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(e.dir, "o.db"))
	if err != nil {
		e.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT action, detail FROM audit ORDER BY id")
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var action, detail string
		if err := rows.Scan(&action, &detail); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, action+": "+detail)
	}
	return out
}

func (e *servicesEnv) hasAudit(want string) bool {
	e.t.Helper()
	for _, row := range e.auditRows() {
		if row == want {
			return true
		}
	}
	return false
}

// withAppConfigs makes dir/apps a config folder holding the fake's key.
func withAppConfigs(t *testing.T) func(*Server) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "apps"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "apps", "fake.key"), []byte(testKey), 0o600); err != nil {
		t.Fatal(err)
	}
	return func(s *Server) { s.Config.AppConfigs = dir }
}

func TestDiscoveryRoutesNeedSessionAndCSRF(t *testing.T) {
	e := newServicesEnv(t, withAppConfigs(t))
	anon := newClientFor(e.client)
	routes := []struct{ method, path, body string }{
		{"GET", "/api/discovery", ""},
		{"POST", "/api/discovery/network", `{"targets":"127.0.0.1"}`},
		{"POST", "/api/discovery/linked", ""},
		{"POST", "/api/discovery/config", ""},
		{"POST", "/api/discovery/docker", ""},
		{"POST", "/api/discovery/x/dismiss", ""},
	}
	for _, r := range routes {
		if code, _, _ := anon.raw(r.method, r.path, r.body, nil); code != 401 {
			t.Errorf("%s %s without a session = %d, want 401", r.method, r.path, code)
		}
		if r.method == "POST" {
			if code, _, _ := e.raw(r.method, r.path, r.body, nil); code != 403 {
				t.Errorf("%s %s without CSRF = %d, want 403", r.method, r.path, code)
			}
		}
	}
	if len(e.srv.discovery.Suggestions()) != 0 || e.app.detects.Load() != 0 {
		t.Fatal("a refused request ran discovery")
	}
}

func TestDiscoveryState(t *testing.T) {
	sources := func(e *servicesEnv) map[string]any {
		code, data, _ := e.do("GET", "/api/discovery", "", nil)
		if code != 200 {
			t.Fatalf("GET /api/discovery = %d %v", code, data)
		}
		if s, ok := data["suggestions"].([]any); !ok || len(s) != 0 {
			t.Fatalf("suggestions = %v", data["suggestions"])
		}
		return data["sources"].(map[string]any)
	}
	e := newServicesEnv(t)
	if s := sources(e); s["network"] != true || s["linked"] != false || s["config"] != false || s["docker"] != false {
		t.Fatalf("sources = %v", s)
	}
	if code, _ := e.send("POST", "/api/discovery/config", ""); code != 404 {
		t.Fatalf("config discovery without a folder = %d, want 404", code)
	}
	e.create("Hub")
	if s := sources(e); s["linked"] != true {
		t.Fatalf("sources with a linking service = %v", s)
	}
	if s := sources(newServicesEnv(t, withAppConfigs(t))); s["config"] != true {
		t.Fatalf("sources with a config folder = %v", s)
	}
	if code, _ := e.send("POST", "/api/discovery/docker", ""); code != 404 {
		t.Fatalf("Docker discovery without Docker = %d, want 404", code)
	}
	if s := sources(newServicesEnv(t, withDocker(&fakeCollector{}))); s["docker"] != true {
		t.Fatalf("sources with Docker = %v", s)
	}
}

func TestDiscoverNetworkThenAdd(t *testing.T) {
	e := newServicesEnv(t)
	if code, _ := e.send("POST", "/api/discovery/network", `{"targets":"8.8.0.0/24"}`); code != 400 {
		t.Fatalf("public range = %d, want 400", code)
	}
	if code, _ := e.send("POST", "/api/discovery/network", `{"targets":" "}`); code != 400 {
		t.Fatalf("no targets = %d, want 400", code)
	}
	code, res := e.discoveryRun("/api/discovery/network", `{"targets":"127.0.0.1"}`)
	want := "http://127.0.0.1:" + strconv.Itoa(e.appPort) + "/"
	if code != 200 || len(res.Suggestions) != 1 || res.Suggestions[0].Type != "fake" || res.Suggestions[0].URL != want ||
		res.Suggestions[0].KeyFound {
		t.Fatalf("scan = %d %+v", code, res)
	}
	if !e.hasAudit("ran network discovery: 127.0.0.1") {
		t.Fatalf("audit = %v", e.auditRows())
	}
	id := res.Suggestions[0].ID

	add := func(extra string) (int, map[string]any) {
		return e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":"Found","url":%q,"suggestion":%q%s}`,
			e.appURL, id, extra))
	}
	if code, data := add(`,"useFoundKey":true`); code != 400 || data["error"] != "no API key was found for this suggestion" {
		t.Fatalf("found key without one = %d %v", code, data)
	}
	if code, data := add(""); code != 400 || data["error"] != "apiKey: required for Fake" {
		t.Fatalf("add without a key = %d %v", code, data)
	}
	code, data := add(fmt.Sprintf(`,"apiKey":%q`, testKey))
	if code != 201 || data["name"] != "Found" {
		t.Fatalf("add = %d %v", code, data)
	}
	if code, _ := add(fmt.Sprintf(`,"apiKey":%q`, testKey)); code != 410 {
		t.Fatalf("second add from the same suggestion = %d, want 410", code)
	}
	// It is registered now, so it is no longer suggested; once removed, it is again.
	if code, res := e.discoveryRun("/api/discovery/network", `{"targets":"127.0.0.1"}`); code != 200 ||
		len(res.Suggestions) != 0 {
		t.Fatalf("rescan = %d %+v", code, res)
	}
	if code, _ := e.send("DELETE", e.path(int64(data["id"].(float64))), ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if code, res := e.discoveryRun("/api/discovery/network", `{"targets":"127.0.0.1"}`); code != 200 ||
		len(res.Suggestions) != 1 || res.Suggestions[0].URL != want {
		t.Fatalf("rescan after removal = %d %+v", code, res)
	}
}

func TestAddForgetsOtherFindsOfTheSameApp(t *testing.T) {
	e := newServicesEnv(t, withAppConfigs(t))
	if _, res := e.discoveryRun("/api/discovery/network", `{"targets":"127.0.0.1"}`); len(res.Suggestions) != 1 {
		t.Fatalf("scan = %+v", res)
	}
	_, res := e.discoveryRun("/api/discovery/config", "")
	if len(res.Suggestions) != 1 {
		t.Fatalf("config = %+v", res)
	}
	if code, data := e.send("POST", "/api/services", fmt.Sprintf(
		`{"type":"fake","name":"Configured","url":%q,"suggestion":%q,"useFoundKey":true}`,
		"http://127.0.0.1:"+strconv.Itoa(e.appPort), res.Suggestions[0].ID)); code != 201 {
		t.Fatalf("add = %d %v", code, data)
	}
	// The network find of the same app at the same address is gone too.
	if code, data, _ := e.do("GET", "/api/discovery", "", nil); code != 200 || len(data["suggestions"].([]any)) != 0 {
		t.Fatalf("discovery = %d %v", code, data)
	}
}

func TestDiscoverConfigAndAddWithFoundKey(t *testing.T) {
	e := newServicesEnv(t, withAppConfigs(t))
	code, res := e.discoveryRun("/api/discovery/config", "")
	if code != 200 || len(res.Suggestions) != 1 {
		t.Fatalf("config = %d %+v", code, res)
	}
	sug := res.Suggestions[0]
	if sug.HostKnown || !sug.KeyFound || sug.KeySource != "fake.key in apps" ||
		sug.URL != "http://:"+strconv.Itoa(e.appPort)+"/" {
		t.Fatalf("suggestion = %+v", sug)
	}
	if !e.hasAudit("ran config-folder discovery: ") {
		t.Fatalf("audit = %v", e.auditRows())
	}

	add := func(typ, url string) (int, map[string]any) {
		return e.send("POST", "/api/services", fmt.Sprintf(
			`{"type":%q,"name":"Configured","url":%q,"suggestion":%q,"useFoundKey":true}`, typ, url, sug.ID))
	}
	// The found key only goes to an app of the suggestion's type.
	if code, data := add("keyless", e.appURL+"/open"); code != 400 ||
		data["error"] != "type must match the suggestion to use its key" {
		t.Fatalf("add as another type = %d %v", code, data)
	}
	if e.keyless.detects.Load() != 0 {
		t.Fatal("the found key was offered to an app of another type")
	}
	// A missing host must be filled in; a failed check leaves the suggestion for another try. Both
	// failures are audited, since they were to use the found key.
	if code, _ := add("fake", sug.URL); code != 400 {
		t.Fatalf("add without a host = %d, want 400", code)
	}
	if code, _ := add("fake", e.appURL+"/other"); code != 422 {
		t.Fatalf("add of the wrong app = %d, want 422", code)
	}
	appHost := strings.TrimPrefix(e.appURL, "http://")
	if !e.hasAudit("add from suggestion failed: fake " + appHost + ": " + adapter.ErrWrongApp.Error()) {
		t.Fatalf("audit = %v", e.auditRows())
	}
	code, data := add("fake", e.appURL)
	if code != 201 {
		t.Fatalf("add with the found key = %d %v", code, data)
	}
	id := int64(data["id"].(float64))
	svc, err := e.srv.Store.ServiceByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if key, err := e.box.Open(svc.SealedKey, keyContext(id)); err != nil || string(key) != testKey {
		t.Fatalf("stored key does not open to the found key: %v", err)
	}
	if !e.hasAudit("added service: Configured (fake); key from fake.key in apps sent to " + appHost) {
		t.Fatalf("audit = %v", e.auditRows())
	}
	if _, ok := e.srv.discovery.Peek(sug.ID); ok {
		t.Fatal("the suggestion is still offered after it was added")
	}
	// The host-less find matches no registered URL, yet it is not suggested again — not even once
	// the service is removed (it can still be added by hand).
	if _, res := e.discoveryRun("/api/discovery/config", ""); len(res.Suggestions) != 0 {
		t.Fatalf("added suggestion came back: %+v", res)
	}
	if code, _ := e.send("DELETE", e.path(id), ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	if _, res := e.discoveryRun("/api/discovery/config", ""); len(res.Suggestions) != 0 {
		t.Fatalf("suggestion came back after the service was removed: %+v", res)
	}
}

func TestConcurrentAddsOfOneSuggestion(t *testing.T) {
	e := newServicesEnv(t, withAppConfigs(t))
	_, res := e.discoveryRun("/api/discovery/config", "")
	if len(res.Suggestions) != 1 {
		t.Fatalf("config = %+v", res)
	}
	const adds = 8
	codes := make(chan int, adds)
	var wg sync.WaitGroup
	for i := range adds {
		wg.Go(func() {
			code, _ := e.send("POST", "/api/services", fmt.Sprintf(
				`{"type":"fake","name":"Copy %d","url":%q,"suggestion":%q,"useFoundKey":true}`, i, e.appURL,
				res.Suggestions[0].ID))
			codes <- code
		})
	}
	wg.Wait()
	close(codes)
	count := map[int]int{}
	for c := range codes {
		count[c]++
	}
	if count[201] != 1 || count[410] != adds-1 {
		t.Fatalf("status codes = %v, want one 201 and the rest 410", count)
	}
	if all, _ := e.srv.Store.Services(context.Background()); len(all) != 1 {
		t.Fatalf("%d services stored, want 1", len(all))
	}
}

func TestDiscoverLinked(t *testing.T) {
	e := newServicesEnv(t)
	e.create("Hub")
	code, res := e.discoveryRun("/api/discovery/linked", "")
	// Hub links a keyless app at /open and a type without adapter, which is dropped.
	if code != 200 || len(res.Suggestions) != 1 || res.Suggestions[0].Type != "keyless" ||
		res.Suggestions[0].URL != e.appURL+"/open" || res.Suggestions[0].Source != "linked" || len(res.Errors) != 0 {
		t.Fatalf("linked = %d %+v", code, res)
	}
	if !e.hasAudit("ran linked-app discovery: ") {
		t.Fatalf("audit = %v", e.auditRows())
	}

	// A linking service whose key no longer opens is reported, not queried.
	if _, err := e.srv.Store.CreateService(context.Background(), store.Service{Type: "fake", Name: "Moved",
		BaseURL: e.appURL}, func(id int64) ([]byte, error) { return e.box.Seal([]byte(testKey), keyContext(id+100)) }); err != nil {
		t.Fatal(err)
	}
	_, res = e.discoveryRun("/api/discovery/linked", "")
	if len(res.Errors) != 1 || res.Errors[0] != (discovery.SourceError{Target: "Moved", Message: errKeyUnreadable.Error()}) {
		t.Fatalf("errors = %+v", res.Errors)
	}
}

func TestDismissSuggestion(t *testing.T) {
	e := newServicesEnv(t)
	_, res := e.discoveryRun("/api/discovery/network", `{"targets":"127.0.0.1"}`)
	if len(res.Suggestions) != 1 {
		t.Fatalf("scan = %+v", res)
	}
	sug := res.Suggestions[0]
	if code, _ := e.send("POST", "/api/discovery/"+sug.ID+"/dismiss", ""); code != 204 {
		t.Fatalf("dismiss = %d", code)
	}
	if code, data := e.send("POST", "/api/discovery/"+sug.ID+"/dismiss", ""); code != 410 {
		t.Fatalf("second dismiss = %d %v", code, data)
	}
	if !e.hasAudit("dismissed suggestion: fake " + sug.URL) {
		t.Fatalf("audit = %v", e.auditRows())
	}
	if _, res := e.discoveryRun("/api/discovery/network", `{"targets":"127.0.0.1"}`); len(res.Suggestions) != 0 {
		t.Fatalf("dismissed suggestion came back: %+v", res)
	}
	if code, _ := e.send("POST", "/api/services", fmt.Sprintf(
		`{"type":"fake","name":"X","url":%q,"apiKey":%q,"suggestion":"nope"}`, e.appURL, testKey)); code != 410 {
		t.Fatalf("add from an unknown suggestion = %d, want 410", code)
	}
	if code, _ := e.send("POST", "/api/services", fmt.Sprintf(
		`{"type":"fake","name":"X","url":%q,"useFoundKey":true}`, e.appURL)); code != 400 {
		t.Fatalf("useFoundKey without a suggestion = %d, want 400", code)
	}
}

func TestDiscoveryOneRunAtATime(t *testing.T) {
	e := newServicesEnv(t, withAppConfigs(t))
	// A linking app whose /linked answers only when the test lets it.
	entered, release := make(chan struct{}), make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/status":
			fmt.Fprint(w, `{"app":"fake","version":"2.0"}`)
		case "/linked":
			close(entered)
			<-release
			fmt.Fprint(w, `[]`)
		}
	}))
	defer slow.Close()
	if code, data := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":"Slow","url":%q,"apiKey":%q}`,
		slow.URL, testKey)); code != 201 {
		t.Fatalf("create = %d %v", code, data)
	}

	done := make(chan int)
	go func() {
		code, _ := e.discoveryRun("/api/discovery/linked", "")
		done <- code
	}()
	<-entered
	for _, path := range []string{"/api/discovery/network", "/api/discovery/linked", "/api/discovery/config"} {
		if code, data := e.send("POST", path, `{"targets":"127.0.0.1"}`); code != 409 ||
			!strings.Contains(fmt.Sprint(data["error"]), "already in progress") {
			t.Errorf("%s during a run = %d %v, want 409", path, code, data)
		}
	}
	close(release)
	if code := <-done; code != 200 {
		t.Fatalf("first run = %d", code)
	}
	if code, _ := e.discoveryRun("/api/discovery/config", ""); code != 200 {
		t.Fatalf("run after the first ended = %d", code)
	}
}

func TestDiscoverDockerThenAddLinksTheContainer(t *testing.T) {
	f := &fakeCollector{}
	option := withDocker(f)
	f.cs[0].Labels = map[string]string{"observarr.type": "fake", "observarr.url": "http://127.0.0.1:1"} // nothing there
	f.cs = append(f.cs, collect.Container{ID: "b2", Name: "found", State: "running", PID: 43,
		Labels: map[string]string{"observarr.type": "fake"}})
	e := newServicesEnv(t, option)
	appURL := e.appURL
	f.mu.Lock()
	f.cs[1].Labels = map[string]string{"observarr.type": "fake", "observarr.url": appURL} // a new map: the old one is shared
	f.mu.Unlock()
	f.events <- collect.Event{Name: "found", Action: "start"}
	// The sampler lists containers at most every 5 s, also after an event.
	eventuallyWithin(t, 10*time.Second, "the labelled container in the list", func() bool {
		cs := e.srv.System.Snapshot().Containers
		return len(cs) == 2 && cs[1].Labels["observarr.url"] == appURL
	})

	code, res := e.discoveryRun("/api/discovery/docker", "")
	if code != 200 || len(res.Suggestions) != 2 {
		t.Fatalf("Docker discovery = %d %+v", code, res)
	}
	byName := map[string]discovery.Suggestion{}
	for _, s := range res.Suggestions {
		byName[s.Container] = s
	}
	if s := byName["app"]; s.Verified || s.Reason == "" || s.Source != "docker" {
		t.Fatalf("unreachable container = %+v, want unverified with a reason", s)
	}
	found := byName["found"]
	if !found.Verified || found.URL != appURL {
		t.Fatalf("reachable container = %+v", found)
	}
	if !e.hasAudit("ran Docker discovery: ") {
		t.Fatalf("audit = %v", e.auditRows())
	}

	code, data := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":"Found","url":%q,"suggestion":%q,"apiKey":%q}`,
		appURL, found.ID, testKey))
	if code != 201 {
		t.Fatalf("add = %d %v", code, data)
	}
	id := int64(data["id"].(float64))
	if svc, _ := e.srv.Store.ServiceByID(context.Background(), id); svc.Container != "found" {
		t.Fatalf("stored link = %q, want the suggestion's container", svc.Container)
	}
	if c := e.serviceState(id).Container; c == nil || c.Name != "found" || c.State != "running" {
		t.Fatalf("brief = %+v", c)
	}
	// Linked now, so it is not suggested again.
	if code, res := e.discoveryRun("/api/discovery/docker", ""); code != 200 || len(res.Suggestions) != 1 {
		t.Fatalf("rerun = %d %+v", code, res)
	}
}
