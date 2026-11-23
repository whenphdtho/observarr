package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"observarr/internal/auth"
	"observarr/internal/config"
)

// securityOf GETs /api/security and decodes it.
func securityOf(t *testing.T, c *client) securityView {
	t.Helper()
	code, raw, _ := c.raw("GET", "/api/security", "", nil)
	if code != 200 {
		t.Fatalf("GET /api/security = %d %s", code, raw)
	}
	var v securityView
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func settingOf(v securityView, id string) accessSetting {
	for _, s := range v.Settings {
		if s.ID == id {
			return s
		}
	}
	return accessSetting{}
}

func TestSecurityNeedsSession(t *testing.T) {
	c := newServer(t, config.Config{})
	if code, _, _ := c.do("GET", "/api/security", "", nil); code != 401 {
		t.Fatalf("GET /api/security without a session = %d, want 401", code)
	}
	if code, _, _ := c.do("POST", "/api/security/sign-out-others", "", nil); code != 401 {
		t.Fatalf("POST without a session = %d, want 401", code)
	}
}

func TestSecurityFailedSignInsAndLockouts(t *testing.T) {
	c := newServer(t, config.Config{})
	c.do("POST", "/api/setup", adminSetup, nil)
	ctx := context.Background()
	st := c.srv.Store
	// Rows as auditFailure writes them, from other addresses, plus noise that must not be counted.
	st.Audit(ctx, "root", "login failed", "3 failed logins from 203.0.113.7 in 15 min")
	st.Audit(ctx, "admin", "login failed", "1 failed login from 203.0.113.7 in 15 min")
	st.Audit(ctx, "<script>", "login failed", "2 failed logins from 203.0.113.7 in 15 min")
	st.Audit(ctx, "(setup)", "setup token wrong", "4 wrong setup tokens from 2001:db8:1::/64 in 15 min")
	st.Audit(ctx, "x", "login failed", "garbled")
	st.Audit(ctx, "x", "login failed", "1 failed login from not-an-address in 15 min")
	for range auth.MaxAttempts { // locks 127.0.0.1 out
		newClientFor(c).do("POST", "/api/login", `{"name":"guest","password":"wrong password!!"}`, nil)
	}

	v := securityOf(t, c)
	f := v.Failed
	if f.Hours != 24 || f.Attempts != 3+1+2+4+auth.MaxAttempts || f.Addresses != 3 || f.Partial {
		t.Fatalf("failed = %+v", f)
	}
	byAddr := map[string]failedGroup{}
	for _, g := range f.Groups {
		byAddr[g.Address] = g
	}
	if g := byAddr["203.0.113.7"]; g.Attempts != 6 || !slices.Equal(g.Users, []string{"(invalid user name)", "admin", "root"}) || g.Locked {
		t.Errorf("203.0.113.7 = %+v", g)
	}
	if g := byAddr["2001:db8:1::/64"]; g.Attempts != 4 || !g.SetupToken || len(g.Users) != 0 {
		t.Errorf("IPv6 group = %+v", g)
	}
	if g := byAddr["127.0.0.1"]; g.Attempts != auth.MaxAttempts || !g.Locked || !slices.Equal(g.Users, []string{"guest"}) {
		t.Errorf("own address = %+v", g)
	}
	if f.Groups[0].Address != "127.0.0.1" { // its latest attempt comes from the limiter: the newest
		t.Errorf("groups not latest first: %+v", f.Groups)
	}
	if len(v.Lockouts) != 1 || v.Lockouts[0].Address != "127.0.0.1" || v.Lockouts[0].Seconds <= 0 ||
		v.Lockouts[0].Seconds > int(auth.LockoutWindow.Seconds()) {
		t.Errorf("lockouts = %+v", v.Lockouts)
	}
	if len(v.SignIns) != 1 || v.SignIns[0].User != "admin" || v.SignIns[0].Address != "127.0.0.1" || v.SignIns[0].Via != "setup" {
		t.Errorf("sign-ins = %+v", v.SignIns)
	}
	if strings.Contains(c.seen.String(), "<script>") {
		t.Error("an attempted user name was passed through unchecked")
	}
}

func TestSecuritySessionsAndSignOutOthers(t *testing.T) {
	c := newServer(t, config.Config{})
	c.do("POST", "/api/setup", adminSetup, nil)
	others := []*client{newClientFor(c), newClientFor(c)}
	for _, o := range others {
		if code, _, _ := o.do("POST", "/api/login", admin, nil); code != 200 {
			t.Fatal("login failed")
		}
	}

	v := securityOf(t, c)
	if v.SessionCount != 3 || len(v.Sessions) != 3 {
		t.Fatalf("sessions = %d %+v", v.SessionCount, v.Sessions)
	}
	current := 0
	for _, s := range v.Sessions {
		if s.Current {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("%d sessions marked current", current)
	}
	if len(v.SignIns) != 3 || v.SignIns[0].Via != "password" {
		t.Errorf("sign-ins = %+v", v.SignIns)
	}

	if code, _, _ := c.do("POST", "/api/security/sign-out-others", "", nil); code != 403 {
		t.Fatalf("without the CSRF token = %d, want 403", code)
	}
	code, data, _ := c.do("POST", "/api/security/sign-out-others", "", map[string]string{"X-CSRF-Token": c.csrf})
	if code != 200 || data["ended"] != float64(2) {
		t.Fatalf("sign out others = %d %v", code, data)
	}
	for _, o := range others {
		if code, _, _ := o.do("GET", "/api/security", "", nil); code != 401 {
			t.Fatalf("another session still works: %d", code)
		}
	}
	if v := securityOf(t, c); v.SessionCount != 1 || !v.Sessions[0].Current {
		t.Fatalf("after: %+v", v.Sessions)
	}
	if rows := (&servicesEnv{client: c}).auditRows(); rows[len(rows)-1] != "signed out other sessions: 2 sessions ended" {
		t.Fatalf("audit rows: %q", rows)
	}
}

func TestSecuritySettings(t *testing.T) {
	c := newServer(t, config.Config{SecureCookies: false})
	c.do("POST", "/api/setup", adminSetup, nil)
	v := securityOf(t, c)
	if s := settingOf(v, "connection"); s.Value != "Plain HTTP" || s.State != "info" {
		t.Errorf("connection = %+v", s)
	}
	if s := settingOf(v, "cookies"); s.Value != "Off" || s.State != "info" {
		t.Errorf("cookies on a loopback address = %+v", s)
	}
	if s := settingOf(v, "proxies"); s.Value != "None" || s.State != "ok" {
		t.Errorf("proxies = %+v", s)
	}
	if s := settingOf(v, "setup"); s.Value != "Used" || s.State != "ok" || v.Setup.Pending {
		t.Errorf("setup = %+v %+v", s, v.Setup)
	}
	if len(v.Settings) != 6 {
		t.Errorf("%d settings", len(v.Settings))
	}

	// A proxy forwards, but is not trusted: one lockout would lock everyone out.
	code, raw, _ := c.raw("GET", "/api/security", "", map[string]string{"X-Forwarded-For": "198.51.100.4"})
	if code != 200 || !strings.Contains(string(raw), "OBSERVARR_TRUSTED_PROXIES is not set") {
		t.Errorf("untrusted forwarding proxy not flagged: %s", raw)
	}
}

func TestSecuritySettingsWarnings(t *testing.T) {
	proxy := netip.MustParsePrefix("127.0.0.1/32")
	s := &Server{Config: config.Config{SecureCookies: false, TrustedProxies: []netip.Prefix{proxy}}}
	r := httptest.NewRequest("GET", "/api/security", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.50") // a public client, through the trusted proxy, over plain HTTP
	settings := func(setup setupState) map[string]accessSetting {
		got := map[string]accessSetting{}
		for _, a := range s.accessSettings(r, setup, 0) {
			got[a.ID] = a
		}
		return got
	}
	got := settings(setupState{FileLeft: true})
	for _, id := range []string{"connection", "cookies", "setup"} {
		if got[id].State != "warn" {
			t.Errorf("%s = %+v, want a warning", id, got[id])
		}
	}
	if got["proxies"].State != "ok" || got["proxies"].Value != "1 set" {
		t.Errorf("proxies = %+v", got["proxies"])
	}
	s.Config.TrustedProxies = []netip.Prefix{proxy, netip.MustParsePrefix("0.0.0.0/0")}
	if got := settings(setupState{}); got["proxies"].State != "warn" || !strings.Contains(got["proxies"].Detail, "0.0.0.0/0") {
		t.Errorf("a trust-everything prefix = %+v", got["proxies"])
	}

	r.Header.Set("X-Forwarded-Proto", "https")
	s.Config.SecureCookies = true
	s.Config.TrustedProxies = []netip.Prefix{proxy}
	for _, a := range s.accessSettings(r, setupState{}, 0) {
		if a.State == "warn" {
			t.Errorf("%s warns: %+v", a.ID, a)
		}
	}
}

func TestSetupFileLeft(t *testing.T) {
	dir := t.TempDir()
	s := &Server{Config: config.Config{DataDir: dir}}
	if s.setupFileLeft() {
		t.Fatal("no file, but reported left")
	}
	if err := os.WriteFile(filepath.Join(dir, "setup-token"), []byte("X"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.setupFileLeft() {
		t.Fatal("file not found")
	}
}
