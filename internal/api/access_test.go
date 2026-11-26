package api

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/auth"
	"observarr/internal/config"
	"observarr/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestSetupNeedsToken(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "setup-token")
	if err := os.WriteFile(tokenFile, []byte(testSetupToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newServer(t, config.Config{}, func(s *Server) { s.SetupTokenFile = tokenFile })
	for _, body := range []string{admin, `{"name":"admin","password":"a long enough password","token":"WRONGWRONGWRONGWRONG"}`} {
		if code, data, _ := c.do("POST", "/api/setup", body, nil); code != 403 || data["error"] != errSetupToken {
			t.Fatalf("setup with %s = %d %v, want 403", body, code, data)
		}
	}
	if _, err := os.Stat(tokenFile); err != nil {
		t.Fatal("a refused setup must keep the token file")
	}
	if code, data, _ := c.do("POST", "/api/setup", adminSetup, nil); code != 200 || data["user"] != "admin" {
		t.Fatalf("setup with the token = %d %v", code, data)
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Fatalf("the used token file is still there: %v", err)
	}
	if code, data, _ := c.do("GET", "/api/session", "", nil); code != 200 || data["user"] != "admin" {
		t.Fatalf("session after setup = %d %v", code, data)
	}
}

func TestWrongSetupTokensLockOut(t *testing.T) {
	c := newServer(t, config.Config{})
	for range auth.MaxAttempts {
		c.do("POST", "/api/setup", admin, nil)
	}
	if code, data, h := c.do("POST", "/api/setup", adminSetup, nil); code != 429 || h.Get("Retry-After") == "" ||
		data["error"] != "too many failed logins — try again in 15 min" {
		t.Fatalf("setup after %d wrong tokens = %d %v, want 429", auth.MaxAttempts, code, data)
	}
}

func TestSetupTokenIgnoresSpacesAndCase(t *testing.T) {
	c := newServer(t, config.Config{})
	body := `{"name":"admin","password":"a long enough password","token":" ` + strings.ToLower(testSetupToken) + `\n"}`
	if code, data, _ := c.do("POST", "/api/setup", body, nil); code != 200 || data["user"] != "admin" {
		t.Fatalf("setup with a lower-case token and spaces = %d %v", code, data)
	}
}

func TestSetupWithoutServerTokenIsRefused(t *testing.T) {
	c := newServer(t, config.Config{}, func(s *Server) { s.SetupToken = "" })
	body := `{"name":"admin","password":"a long enough password","token":""}`
	if code, _, _ := c.do("POST", "/api/setup", body, nil); code != 403 {
		t.Fatalf("setup when the server has no token = %d, want 403", code)
	}
}

func TestLoginNeedsHTTPS(t *testing.T) {
	c := newServer(t, config.Config{SecureCookies: true})
	for _, path := range []string{"/api/setup", "/api/login"} {
		if code, data, _ := c.do("POST", path, adminSetup, nil); code != 400 || data["error"] != errNeedsHTTPS {
			t.Fatalf("%s over plain HTTP = %d %v, want 400", path, code, data)
		}
	}

	trusted := config.Config{SecureCookies: true, TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
	p := newServer(t, trusted)
	if code, _, _ := p.do("POST", "/api/setup", adminSetup, map[string]string{"X-Forwarded-Proto": "http"}); code != 400 {
		t.Fatalf("setup forwarded as http = %d, want 400", code)
	}
	if code, data, _ := p.do("POST", "/api/setup", adminSetup, map[string]string{"X-Forwarded-Proto": "https"}); code != 200 {
		t.Fatalf("setup forwarded as https by a trusted proxy = %d %v", code, data)
	}
	if code, _, _ := p.do("POST", "/api/login", admin, map[string]string{"X-Forwarded-Proto": "https"}); code != 200 {
		t.Fatalf("login forwarded as https by a trusted proxy = %d", code)
	}
}

func TestLoginLockoutMessage(t *testing.T) {
	c := newServer(t, config.Config{})
	c.do("POST", "/api/setup", adminSetup, nil)
	for range auth.MaxAttempts {
		c.do("POST", "/api/login", `{"name":"admin","password":"wrong password!!"}`, nil)
	}
	if code, data, _ := c.do("POST", "/api/login", admin, nil); code != 429 ||
		data["error"] != "too many failed logins — try again in 15 min" {
		t.Fatalf("locked-out login = %d %v", code, data)
	}
}

func TestProxyHeaderNeedsProxyAuth(t *testing.T) {
	trusted := config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, ProxyUserHeader: "Remote-User"}
	c := newServer(t, trusted)
	c.do("POST", "/api/setup", adminSetup, nil)
	if code, _, _ := newClientFor(c).do("GET", "/api/session", "", map[string]string{"Remote-User": "alice"}); code != 401 {
		t.Fatalf("user header from a trusted proxy without OBSERVARR_PROXY_AUTH = %d, want 401", code)
	}
	// Without proxy login, the header does not stand between a session and its user either.
	if code, _, _ := c.do("POST", "/api/logout", "", map[string]string{"X-CSRF-Token": c.csrf, "Remote-User": "alice"}); code != 200 {
		t.Fatalf("logout with an ignored user header = %d, want 200", code)
	}
}

func TestProxyAuthRefusesDirectPeers(t *testing.T) {
	proxied := config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, ProxyAuth: true,
		ProxyUserHeader: "Remote-User"} // the test client, at 127.0.0.1, is not the proxy
	c := newServer(t, proxied)
	for _, path := range []string{"/api/setup", "/api/login"} {
		if code, data, _ := c.do("POST", path, adminSetup, nil); code != 403 || data["error"] != errViaProxy {
			t.Fatalf("%s from outside the proxy = %d %v, want 403", path, code, data)
		}
	}

	// A session cookie, however it was obtained, does not get around the proxy either.
	ctx := context.Background()
	u, err := c.srv.Store.CreateFirstUser(ctx, "admin", noPassword)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if _, err := c.srv.Sessions.Start(ctx, rec, u.ID); err != nil {
		t.Fatal(err)
	}
	base, _ := url.Parse(c.base)
	c.http.Jar.SetCookies(base, rec.Result().Cookies())
	for _, path := range []string{"/api/session", "/api/services", "/api/events"} {
		if code, data, _ := c.do("GET", path, "", map[string]string{"Remote-User": "admin"}); code != 403 || data["error"] != errViaProxy {
			t.Fatalf("%s from outside the proxy with a session = %d %v, want 403", path, code, data)
		}
	}
	if code, _, _ := c.do("GET", "/api/health", "", nil); code != 200 {
		t.Fatalf("health from outside the proxy = %d, want 200", code)
	}
}

func TestFailuresAuditedPerUserUntilALogin(t *testing.T) {
	c := newServer(t, config.Config{})
	c.do("POST", "/api/setup", adminSetup, nil)
	fail := func(name string) {
		c.do("POST", "/api/login", `{"name":"`+name+`","password":"wrong password!!"}`, nil)
	}
	fail("admin")
	fail("bob")
	fail("admin")
	if code, _, _ := newClientFor(c).do("POST", "/api/login", admin, nil); code != 200 {
		t.Fatalf("login = %d", code)
	}
	fail("admin")
	want := []string{
		"created the admin account: from 127.0.0.1",
		"login failed: 2 failed logins from 127.0.0.1 in 15 min",
		"login failed: 1 failed login from 127.0.0.1 in 15 min", // bob
		"logged in: from 127.0.0.1",
		"login failed: 1 failed login from 127.0.0.1 in 15 min", // a new row after the login
	}
	if got := (&servicesEnv{client: c}).auditRows(); !slices.Equal(got, want) {
		t.Fatalf("audit rows:\n%q\nwant\n%q", got, want)
	}
}

func TestFailuresAuditedOncePerWindow(t *testing.T) {
	c := newServer(t, config.Config{})
	wrongToken := `{"name":"admin","password":"a long enough password","token":"WRONGWRONGWRONGWRONG"}`
	for range 3 {
		c.do("POST", "/api/setup", wrongToken, nil)
	}
	c.do("POST", "/api/setup", adminSetup, nil)
	for range 2 {
		newClientFor(c).do("POST", "/api/login", admin, nil)
	}
	for range auth.MaxAttempts {
		c.do("POST", "/api/login", `{"name":"admin","password":"wrong password!!"}`, nil)
	}
	rows := func() []string { return (&servicesEnv{client: c}).auditRows() }
	want := []string{
		"setup token wrong: 3 wrong setup tokens from 127.0.0.1 in 15 min",
		"created the admin account: from 127.0.0.1",
		"logged in: from 127.0.0.1",
		"logged in: from 127.0.0.1",
		"login failed: 5 failed logins from 127.0.0.1 in 15 min",
	}
	if got := rows(); !slices.Equal(got, want) {
		t.Fatalf("audit rows:\n%q\nwant\n%q", got, want)
	}

	c.srv.failMu.Lock()
	for key, f := range c.srv.failures { // the window ends
		f.since = f.since.Add(-failAuditGap)
		c.srv.failures[key] = f
	}
	c.srv.failMu.Unlock()
	if err := c.srv.Limiter.Reset(context.Background(), "127.0.0.1"); err != nil { // and with it the lockout
		t.Fatal(err)
	}
	c.do("POST", "/api/login", `{"name":"admin","password":"wrong password!!"}`, nil)
	if got := rows(); len(got) != len(want)+1 || got[len(want)] != "login failed: 1 failed login from 127.0.0.1 in 15 min" {
		t.Fatalf("audit rows after the window:\n%q", got)
	}
}

func TestProxyLoginAuditedOncePerHour(t *testing.T) {
	trusted := config.Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, ProxyAuth: true,
		ProxyUserHeader: "Remote-User"}
	c := newServer(t, trusted)
	for range 3 { // a client that keeps no cookie
		if code, _, _ := newClientFor(c).do("GET", "/api/session", "", map[string]string{"Remote-User": "alice"}); code != 200 {
			t.Fatalf("proxy session = %d", code)
		}
	}
	var logins int
	for _, row := range (&servicesEnv{client: c}).auditRows() {
		if strings.HasPrefix(row, "logged in via trusted proxy") {
			logins++
		}
	}
	if logins != 1 {
		t.Fatalf("%d proxy logins audited, want 1", logins)
	}
}

func TestHealthReportsAFailingDatabase(t *testing.T) {
	c := newServer(t, config.Config{})
	if code, data, _ := c.do("GET", "/api/health", "", nil); code != 200 || data["status"] != "ok" {
		t.Fatalf("health = %d %v", code, data)
	}
	c.srv.Store.Close()
	c.srv.healthAt = c.srv.healthAt.Add(-healthEvery) // the next request probes again
	if code, data, _ := c.do("GET", "/api/health", "", nil); code != 503 || data["status"] != "degraded" || data["reason"] == "" {
		t.Fatalf("health with a closed database = %d %v, want 503", code, data)
	}
}

// TestLockedDatabaseKeepsServingReads holds the database's write lock from outside, as another process could: the
// health check reports it degraded, changes fail, and everything that only reads keeps working.
func TestLockedDatabaseKeepsServingReads(t *testing.T) {
	t.Parallel() // each write waits out the busy timeout
	e := newServicesEnv(t)
	e.create("App")
	other, err := sql.Open("sqlite", "file:"+filepath.Join(e.dir, "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	lock, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	e.srv.healthAt = time.Time{} // the next health check probes again

	if code, data, _ := e.do("GET", "/api/health", "", nil); code != 503 || data["status"] != "degraded" {
		t.Fatalf("health while locked = %d %v, want 503 degraded", code, data)
	}
	if code, _, _ := e.do("GET", "/api/security", "", nil); code != 200 { // read from the database
		t.Fatalf("security while locked = %d", code)
	}
	if code, _, _ := e.do("GET", "/api/session", "", nil); code != 200 {
		t.Fatalf("session while locked = %d", code)
	}
	if code, _ := e.send("POST", "/api/services", fmt.Sprintf(`{"type":"fake","name":"B","url":%q,"apiKey":%q}`,
		e.appURL, testKey)); code < 500 {
		t.Fatalf("adding a service while locked = %d, want a server error", code)
	}

	if _, err := lock.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	e.srv.healthAt = time.Time{}
	if code, _, _ := e.do("GET", "/api/health", "", nil); code != 200 {
		t.Fatalf("health after the lock was released = %d", code)
	}
}

func TestReadOnlyDatabaseIs503(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permission bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "o.db")
	st, err := store.Open(context.Background(), path, quiet)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	_, err = store.Open(context.Background(), path, quiet) // a real SQLITE_READONLY error
	if store.Degraded(err) == "" {
		t.Skipf("no read-only error here: %v", err)
	}
	s := &Server{Log: quiet}
	w := httptest.NewRecorder()
	s.internal(w, httptest.NewRequest("POST", "/api/services", nil), err)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "database is read-only") {
		t.Fatalf("read-only error = %d %s, want 503", w.Code, w.Body)
	}
}

func TestFoundKeyGoesOnlyWhereFound(t *testing.T) {
	found := adapter.Candidate{BaseURL: &url.URL{Scheme: "http", Host: "Sonarr.lan:8989"}}
	for raw, want := range map[string]bool{
		"http://sonarr.lan:8989":         true,
		"http://sonarr.lan:8989/sonarr/": true, // the path may differ
		"https://sonarr.lan:8989":        false,
		"http://sonarr.lan:8990":         false,
		"http://evil.example:8989":       false,
		"not a url":                      true, // refused by validation instead
	} {
		if got := foundKeyMayGo(raw, found); got != want {
			t.Errorf("foundKeyMayGo(%q) = %v, want %v", raw, got, want)
		}
	}
	hostless := adapter.Candidate{BaseURL: &url.URL{Scheme: "http", Host: ":8989"}}
	if !foundKeyMayGo("http://anything.lan:1", hostless) {
		t.Error("a key found without a host must go where the user says")
	}
	if origin(&url.URL{Scheme: "https", Host: "a.lan"}) != origin(&url.URL{Scheme: "https", Host: "A.lan:443"}) {
		t.Error("the default port must not make a different origin")
	}
}

func TestWrongAppIsNamed(t *testing.T) {
	if got := detectError("h:1", fmt.Errorf("probe: %w", adapter.WrongApp("Radarr"))); got != "Radarr answers at this address" {
		t.Fatalf("detectError = %q", got)
	}
	if got := detectError("h:1", adapter.ErrWrongApp); got != adapter.ErrWrongApp.Error() {
		t.Fatalf("detectError = %q", got)
	}
}
