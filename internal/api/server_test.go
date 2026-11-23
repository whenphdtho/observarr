package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"observarr/internal/auth"
	"observarr/internal/config"
	"observarr/internal/store"
)

type client struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
	srv  *Server
	dir  string      // the server's data directory
	logs *syncBuffer // everything the server logged
	seen *syncBuffer // every response body this client, and clients made from it, received
}

// syncBuffer is a bytes.Buffer that is safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// newServer starts a test server. Options may set further Server fields before the handler is built.
func newServer(t *testing.T, cfg config.Config, options ...func(*Server)) *client {
	t.Helper()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "o.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	limiter, err := auth.OpenLimiter(context.Background(), st, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Log: log, Store: st, Config: cfg, Version: "test", SetupToken: testSetupToken,
		Sessions: &auth.Sessions{Store: st, Secure: false, Now: time.Now},
		Limiter:  limiter,
		UI:       fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}, "assets/app-1.js": {Data: []byte("js")}}}
	for _, o := range options {
		o(s)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, base: ts.URL, http: &http.Client{Jar: jar}, srv: s, dir: dir, logs: logs, seen: &syncBuffer{}}
}

func (c *client) do(method, path, body string, header map[string]string) (int, map[string]any, http.Header) {
	c.t.Helper()
	code, raw, h := c.raw(method, path, body, header)
	var data map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &data); err != nil {
			c.t.Fatalf("%s %s: response is not a JSON object: %q", method, path, raw)
		}
	}
	if s, ok := data["csrf"].(string); ok {
		c.csrf = s
	}
	return code, data, h
}

// raw sends a request and returns the status, the body and the headers.
func (c *client) raw(method, path, body string, header map[string]string) (int, []byte, http.Header) {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.base+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	c.seen.Write(raw)
	return resp.StatusCode, raw, resp.Header
}

const (
	testSetupToken = "ABCDEFGHIJKLMNOPQRST"
	admin          = `{"name":"admin","password":"a long enough password"}`
	adminSetup     = `{"name":"admin","password":"a long enough password","token":"` + testSetupToken + `"}`
)

func TestHealthAndSecurityHeaders(t *testing.T) {
	c := newServer(t, config.Config{})
	code, data, h := c.do("GET", "/api/health", "", nil)
	if code != 200 || data["status"] != "ok" {
		t.Fatalf("health = %d %v", code, data)
	}
	for _, k := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if h.Get(k) == "" {
			t.Errorf("missing header %s", k)
		}
	}
}

func TestSetupOnceThenLogin(t *testing.T) {
	c := newServer(t, config.Config{})
	if code, data, _ := c.do("GET", "/api/session", "", nil); code != 200 || data["setup"] != true {
		t.Fatalf("fresh install session = %d %v, want setup", code, data)
	}
	if code, _, _ := c.do("POST", "/api/setup", `{"name":"admin","password":"short"}`, nil); code != 400 {
		t.Fatalf("weak password = %d, want 400", code)
	}
	if code, data, _ := c.do("POST", "/api/setup", adminSetup, nil); code != 200 || data["user"] != "admin" || c.csrf == "" {
		t.Fatalf("setup = %d %v", code, data)
	}
	if code, _, _ := c.do("POST", "/api/setup", `{"name":"evil","password":"another long password"}`, nil); code != 409 {
		t.Fatalf("second setup = %d, want 409", code)
	}

	other := newClientFor(c)
	if code, _, _ := other.do("POST", "/api/login", `{"name":"admin","password":"wrong password!!"}`, nil); code != 401 {
		t.Fatalf("wrong password = %d, want 401", code)
	}
	if code, data, _ := other.do("POST", "/api/login", admin, nil); code != 200 || data["user"] != "admin" {
		t.Fatalf("login = %d %v", code, data)
	}
	known, err := c.srv.Store.KnownLogins(context.Background(), time.Now().Add(-time.Minute))
	if _, ok := known["127.0.0.1"]; err != nil || !ok {
		t.Fatalf("known login addresses = %v, %v; want the login's address stored", known, err)
	}
}

func newClientFor(c *client) *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: c.t, base: c.base, http: &http.Client{Jar: jar}, srv: c.srv, dir: c.dir, logs: c.logs, seen: c.seen}
}

func TestLoginLockout(t *testing.T) {
	c := newServer(t, config.Config{})
	c.do("POST", "/api/setup", adminSetup, nil)
	for i := 0; i < auth.MaxAttempts; i++ {
		c.do("POST", "/api/login", `{"name":"admin","password":"wrong password!!"}`, nil)
	}
	code, _, h := c.do("POST", "/api/login", admin, nil) // even the right password is refused now
	if wait, err := strconv.Atoi(h.Get("Retry-After")); code != 429 || err != nil || wait < 1 {
		t.Fatalf("after %d failed logins = %d (Retry-After %q), want 429", auth.MaxAttempts, code, h.Get("Retry-After"))
	}
}

func TestProxyCreatedUserHasNoPassword(t *testing.T) {
	trusted := config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, ProxyAuth: true, ProxyUserHeader: "Remote-User"}
	c := newServer(t, trusted)
	c.do("POST", "/api/setup", adminSetup, nil)
	if code, _, _ := newClientFor(c).do("GET", "/api/session", "", map[string]string{"Remote-User": "alice"}); code != 200 {
		t.Fatalf("proxy session = %d", code)
	}
	for _, password := range []string{"!", "", "a long enough password"} {
		body := `{"name":"alice","password":"` + password + `"}`
		if code, _, _ := newClientFor(c).do("POST", "/api/login", body, nil); code != 401 {
			t.Fatalf("password login for a proxy-only user with %q = %d, want 401", password, code)
		}
	}
}

func TestJSONRejectsTrailingData(t *testing.T) {
	c := newServer(t, config.Config{})
	if code, _, _ := c.do("POST", "/api/setup", adminSetup+` {"name":"evil"}`, nil); code != 400 {
		t.Fatalf("body with trailing data = %d, want 400", code)
	}
}

func TestHashSlotGivesUpWhenRequestEnds(t *testing.T) {
	s := &Server{hashSlots: make(chan struct{}, 1)}
	s.hashSlots <- struct{}{} // the only slot is busy
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	if s.withHashSlot(ctx, func() { ran = true }) || ran {
		t.Fatal("withHashSlot ran fn after the request was cancelled")
	}
	<-s.hashSlots
	if !s.withHashSlot(context.Background(), func() { ran = true }) || !ran || len(s.hashSlots) != 0 {
		t.Fatal("withHashSlot must run fn in a free slot and release it")
	}
}

func TestLogoutNeedsCSRF(t *testing.T) {
	c := newServer(t, config.Config{})
	c.do("POST", "/api/setup", adminSetup, nil)
	if code, _, _ := c.do("HEAD", "/api/logout", "", nil); code == 200 {
		t.Fatal("HEAD must not reach logout (it would skip the CSRF check)")
	}
	if code, _, _ := c.do("POST", "/api/logout", "", nil); code != 403 {
		t.Fatalf("logout without CSRF = %d, want 403", code)
	}
	if code, _, _ := c.do("POST", "/api/logout", "", map[string]string{"X-CSRF-Token": c.csrf}); code != 200 {
		t.Fatalf("logout = %d, want 200", code)
	}
	if code, _, _ := c.do("GET", "/api/session", "", nil); code != 401 {
		t.Fatalf("session after logout = %d, want 401", code)
	}
}

func TestJSONRequired(t *testing.T) {
	c := newServer(t, config.Config{})
	req, _ := http.NewRequest("POST", c.base+"/api/login", strings.NewReader("name=admin&password=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded") // what a cross-site form would send
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("form post = %d, want 415", resp.StatusCode)
	}
}

func TestTrustedProxyLogin(t *testing.T) {
	trusted := config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, ProxyAuth: true, ProxyUserHeader: "Remote-User"}
	c := newServer(t, trusted)
	c.do("POST", "/api/setup", adminSetup, nil)
	p := newClientFor(c)
	if code, data, _ := p.do("GET", "/api/session", "", map[string]string{"Remote-User": "alice"}); code != 200 || data["user"] != "alice" {
		t.Fatalf("proxy session = %d %v", code, data)
	}

	untrusted := newServer(t, config.Config{ProxyUserHeader: "Remote-User"})
	untrusted.do("POST", "/api/setup", adminSetup, nil)
	q := newClientFor(untrusted)
	if code, _, _ := q.do("GET", "/api/session", "", map[string]string{"Remote-User": "alice"}); code != 401 {
		t.Fatalf("header without trusted proxy = %d, want 401", code)
	}
}

func TestUIRoutes(t *testing.T) {
	c := newServer(t, config.Config{})
	for path, want := range map[string]string{"/": "<html>app</html>", "/apps": "<html>app</html>", "/assets/app-1.js": "js", "/assets/": "<html>app</html>"} {
		resp, err := c.http.Get(c.base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != want {
			t.Errorf("GET %s = %q, want %q", path, body, want)
		}
	}
	if code, _, _ := c.do("GET", "/api/nope", "", nil); code != 404 {
		t.Fatalf("unknown API path = %d, want 404", code)
	}
	if code, _, _ := c.do("POST", "/apps", "", nil); code != 405 {
		t.Fatalf("POST to a UI path = %d, want 405", code)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := recoverPanics(log, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 500 {
		t.Fatalf("panic = %d, want 500", w.Code)
	}
}

func TestSetupDoneIsRefusedWithoutHashing(t *testing.T) {
	c := newServer(t, config.Config{})
	c.do("POST", "/api/setup", adminSetup, nil)
	for range cap(c.srv.hashSlots) { // a setup that still hashed would now wait for a slot until the timeout
		c.srv.hashSlots <- struct{}{}
	}
	c.http.Timeout = 2 * time.Second
	if code, _, _ := c.do("POST", "/api/setup", `{"name":"evil","password":"another long password"}`, nil); code != 409 {
		t.Fatalf("setup after setup = %d, want 409", code)
	}
}

func TestPasswordLengthCountsCharacters(t *testing.T) {
	c := newServer(t, config.Config{})
	if code, _, _ := c.do("POST", "/api/setup", `{"name":"admin","password":"ääääää"}`, nil); code != 400 {
		t.Fatalf("6 characters (12 bytes) = %d, want 400", code)
	}
	if code, _, _ := c.do("POST", "/api/setup", `{"name":"admin","password":"`+strings.Repeat("x", 1025)+`"}`, nil); code != 400 {
		t.Fatalf("1025 bytes = %d, want 400", code)
	}
	if code, _, _ := c.do("POST", "/api/setup", `{"name":"admin","password":"ääääääääääää","token":"`+testSetupToken+`"}`, nil); code != 200 {
		t.Fatalf("12 characters = %d, want 200", code)
	}
}

func TestLatePanicAbortsResponse(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(recoverPanics(log, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("partial"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err) // the recovery wrapper must not hide the real writer
		}
		panic("boom")
	})))
	defer ts.Close()
	resp, err := http.Get(ts.URL)
	if err != nil {
		return // aborted before the headers arrived
	}
	defer resp.Body.Close()
	if body, err := io.ReadAll(resp.Body); err == nil {
		t.Fatalf("response after a late panic completed normally: %q", body)
	}
}

func TestFlushAlonePanicAbortsResponse(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(recoverPanics(log, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err) // the recovery wrapper must not hide the real writer
		}
		panic("boom") // never called Write: only a flush marks the response as started
	})))
	defer ts.Close()
	resp, err := http.Get(ts.URL)
	if err != nil {
		return // aborted before the headers arrived
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil && strings.Contains(string(body), `{"error"`) {
		t.Fatalf("flush-then-panic response was completed with an error body: %q", body)
	}
}

func TestMissingAssetIs404(t *testing.T) {
	c := newServer(t, config.Config{})
	if code, data, _ := c.do("GET", "/assets/missing.js", "", nil); code != 404 || data["error"] != "not found" {
		t.Fatalf("missing asset = %d %v, want 404", code, data)
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for path, immutable := range map[string]bool{"/assets/app-1.js": true, "/assets/app-1.js/": false} {
		resp, err := noFollow.Get(c.base + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := strings.Contains(resp.Header.Get("Cache-Control"), "immutable"); got != immutable {
			t.Errorf("GET %s: immutable cache = %v, want %v", path, got, immutable)
		}
	}
}

func TestNewSessionEndsTheOldOne(t *testing.T) {
	c := newServer(t, config.Config{})
	base, _ := url.Parse(c.base)
	token := func() string {
		for _, k := range c.http.Jar.Cookies(base) {
			if k.Name == c.srv.Sessions.CookieName() {
				return k.Value
			}
		}
		t.Fatal("no session cookie")
		return ""
	}
	sessionCode := func(tok string) int {
		req, _ := http.NewRequest("GET", c.base+"/api/session", nil)
		req.AddCookie(&http.Cookie{Name: c.srv.Sessions.CookieName(), Value: tok})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	c.do("POST", "/api/setup", adminSetup, nil)
	first := token()
	if code, _, h := c.do("POST", "/api/login", admin, nil); code != 200 || len(h.Values("Set-Cookie")) != 1 {
		t.Fatalf("login = %d with %d Set-Cookie headers, want 200 with exactly one", code, len(h.Values("Set-Cookie")))
	}
	second := token()
	if first == second || sessionCode(first) != 401 || sessionCode(second) != 200 {
		t.Fatal("logging in again must end the previous session and keep only the new one")
	}
}

func TestSessionOfAnotherUserThanTheProxyAsserts(t *testing.T) {
	trusted := config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, ProxyAuth: true, ProxyUserHeader: "Remote-User"}
	c := newServer(t, trusted)
	c.do("POST", "/api/setup", adminSetup, nil)
	if code, _, _ := c.do("POST", "/api/logout", "", map[string]string{"X-CSRF-Token": c.csrf, "Remote-User": "alice"}); code != 401 {
		t.Fatalf("admin's session with the proxy asserting alice = %d, want 401", code)
	}
}

func TestConcurrentSetupCreatesOneAccount(t *testing.T) {
	c := newServer(t, config.Config{})
	const n = 20
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			resp, err := http.Post(c.base+"/api/setup", "application/json",
				strings.NewReader(`{"name":"admin`+strconv.Itoa(i)+`","password":"a long enough password","token":"`+testSetupToken+`"}`))
			if err != nil {
				return // codes[i] stays 0 and fails the count below
			}
			resp.Body.Close()
			codes[i] = resp.StatusCode
		})
	}
	wg.Wait()
	var ok, refused int
	for _, code := range codes {
		switch code {
		case 200:
			ok++
		case 409, 429: // setup done, or too many attempts from this address
			refused++
		}
	}
	if ok != 1 || refused != n-1 {
		t.Fatalf("concurrent setups: %v, want one 200 and the rest 409 or 429", codes)
	}
}

func TestJSONWithCharsetAccepted(t *testing.T) {
	c := newServer(t, config.Config{})
	if code, _, _ := c.do("POST", "/api/setup", adminSetup, map[string]string{"Content-Type": "application/json; charset=utf-8"}); code != 200 {
		t.Fatalf("setup with charset = %d, want 200", code)
	}
}

func TestBodyTooLarge(t *testing.T) {
	c := newServer(t, config.Config{})
	body := `{"name":"` + strings.Repeat("x", maxJSONBody) + `","password":"a long enough password"}`
	if code, _, _ := c.do("POST", "/api/setup", body, nil); code != 413 {
		t.Fatalf("oversized body = %d, want 413", code)
	}
}
