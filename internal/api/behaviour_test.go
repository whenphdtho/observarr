package api

import (
	"encoding/json"
	"strings"
	"testing"

	"observarr/internal/alert"
	"observarr/internal/auth"
	"observarr/internal/config"
)

// Sending a channel's settings or secrets again, as they are saved, changes nothing: the channel stays tested and
// on. Only a real change needs a new test.
func TestUnchangedChannelStaysOn(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	srv, _ := hook(t)
	id := e.addHook(srv.URL, "Hook")
	e.send("POST", channelPath(id)+"/test", "")
	if code, data := e.send("PUT", channelPath(id), `{"enabled":true}`); code != 200 || data["enabled"] != true {
		t.Fatalf("enable = %d %v", code, data)
	}
	same, _ := json.Marshal(map[string]any{"settings": map[string]any{}, "secrets": map[string]string{"url": srv.URL + "/" + testKey, "secret": testKey}})
	for _, body := range []string{string(same), `{"settings":{}}`, `{"name":"Hook"}`} {
		if code, data := e.send("PUT", channelPath(id), body); code != 200 || data["enabled"] != true || data["tested"] != true {
			t.Fatalf("resending what is saved (%s) = %d %v, want it still on", body, code, data)
		}
	}
	if e.hasAudit("changed alert channel: Hook: settings changed, disabled until tested") {
		t.Fatalf("an unchanged channel was noted as changed: %v", e.auditRows())
	}
	// Another signing secret is a change.
	other, _ := json.Marshal(map[string]any{"secrets": map[string]string{"secret": strings.Repeat("x", 20)}})
	if code, data := e.send("PUT", channelPath(id), string(other)); code != 200 || data["enabled"] != false || data["tested"] != false {
		t.Fatalf("a changed secret = %d %v, want it off until tested", code, data)
	}
}

// A rule the request leaves out keeps its value; only an explicit false switches one off.
func TestRulesLeftOutKeepTheirValue(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	rules := func(enabled string) string {
		return `{"enabled":` + enabled + `,"downMinutes":3,"fullPercent":90,"failedSignIns":10,"repeatHours":0,"perHour":30,
			"quiet":{"on":false,"from":"22:00","to":"07:00"},"link":""}`
	}
	if code, data := e.send("PUT", "/api/alerts/rules", rules(`{}`)); code != 200 {
		t.Fatalf("rules without any switch = %d %v", code, data)
	}
	got := e.srv.Alerts.Rules()
	want := alert.DefaultRules()
	for _, rule := range alert.AllRules {
		if got.Enabled[rule] != want.Enabled[rule] {
			t.Errorf("%s = %v after a request that left it out, want %v", rule, got.Enabled[rule], want.Enabled[rule])
		}
	}
	if code, data := e.send("PUT", "/api/alerts/rules", rules(`{"down":false,"requests":true}`)); code != 200 {
		t.Fatalf("rules with two switches = %d %v", code, data)
	}
	got = e.srv.Alerts.Rules()
	if got.Enabled[alert.RuleDown] || !got.Enabled[alert.RuleRequests] || !got.Enabled[alert.RuleHealth] {
		t.Fatalf("enabled = %v, want down off, requests on, the rest as they were", got.Enabled)
	}
	code, data, _ := e.do("GET", "/api/alerts/rules", "", nil)
	if on := data["enabled"].(map[string]any); code != 200 || len(on) != len(alert.AllRules) {
		t.Fatalf("saved switches = %v, want one for every rule", data["enabled"])
	}
	if code, _ := e.send("PUT", "/api/alerts/rules", rules(`{"nope":true}`)); code != 400 {
		t.Fatalf("an unknown rule = %d", code)
	}
}

// Length is the rule; a password that is long and still anyone's first guess is refused all the same.
func TestSetupRefusesGuessablePasswords(t *testing.T) {
	for _, pw := range []string{"aaaaaaaaaaaa", "111111111111111111", "Password1234", "123456789012", "qwertyuiopasdf"} {
		if !auth.WeakPassword(pw) {
			t.Errorf("%q is not seen as weak", pw)
		}
	}
	for _, pw := range []string{"correct horse battery", "aaaaaaaaaaab", "demo-password-2026", ""} {
		if auth.WeakPassword(pw) {
			t.Errorf("%q is seen as weak", pw)
		}
	}
	c := newServer(t, config.Config{})
	weak := strings.Replace(adminSetup, "a long enough password", "aaaaaaaaaaaa", 1)
	if code, data, _ := c.do("POST", "/api/setup", weak, nil); code != 400 || !strings.HasPrefix(data["error"].(string), "password: too easy to guess") {
		t.Fatalf("setup with one repeated character = %d %v", code, data)
	}
	if code, data, _ := c.do("POST", "/api/setup", adminSetup, nil); code != 200 {
		t.Fatalf("setup with a good password afterwards = %d %v", code, data)
	}
}

// A request refused before any password was checked is no failed login: it does not count towards the lockout.
func TestRefusedRequestsAreNoFailedLogins(t *testing.T) {
	c := newServer(t, config.Config{})
	c.do("POST", "/api/setup", adminSetup, nil)
	c.do("POST", "/api/logout", "", nil)
	long := `{"name":"admin","password":"` + strings.Repeat("x", maxPasswordLen+1) + `"}`
	for range 3 * auth.MaxAttempts {
		if code, _, _ := c.raw("POST", "/api/login", `{"name":"admin"`, map[string]string{"Content-Type": "application/json"}); code != 400 {
			t.Fatalf("broken JSON = %d", code)
		}
		if code, _, _ := c.raw("POST", "/api/login", admin, map[string]string{"Content-Type": "text/plain"}); code != 415 {
			t.Fatalf("wrong content type = %d", code)
		}
		if code, _, _ := c.do("POST", "/api/login", long, nil); code != 400 {
			t.Fatalf("over-long password = %d", code)
		}
	}
	// Every real guess still counts: the last one allowed is taken, the next is refused.
	for range auth.MaxAttempts - 1 {
		if code, _, _ := c.do("POST", "/api/login", `{"name":"admin","password":"wrong password!!"}`, nil); code != 401 {
			t.Fatalf("a wrong password = %d, want 401: refused requests used up the attempts", code)
		}
	}
	if code, data, _ := c.do("POST", "/api/login", admin, nil); code != 200 {
		t.Fatalf("the right password on the last attempt = %d %v", code, data)
	}
}

// A session has a stream for each of five tabs. One more is told why it gets none, the session's state says so
// (a page cannot read a refused stream's answer), and what needs attention can be asked for instead.
func TestATabTooManyIsToldWhy(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	ctx := timeout(t)
	// A browser has six connections to one server over HTTP/1.1: the streams must leave one of them free.
	if maxSessionStreams != 5 {
		t.Fatalf("streams per session = %d, want 5", maxSessionStreams)
	}
	var streams []*stream
	for range maxSessionStreams - 1 {
		streams = append(streams, e.openEvents(ctx))
	}
	if _, data, _ := e.do("GET", "/api/session", "", nil); data["streamsFull"] != nil {
		t.Fatalf("session with a stream to spare = %v", data)
	}
	streams = append(streams, e.openEvents(ctx))
	code, data, _ := e.do("GET", "/api/events", "", nil)
	if code != 503 || data["error"] != "too many open tabs: close one to get live updates here" {
		t.Fatalf("one stream too many = %d %v", code, data)
	}
	if _, data, _ := e.do("GET", "/api/session", "", nil); data["streamsFull"] != true {
		t.Fatalf("session without a stream to spare = %v", data)
	}
	// Without a stream the page asks: the services, and what needs attention.
	if code, raw, _ := e.raw("GET", "/api/alerts/attention", "", nil); code != 200 || !strings.HasPrefix(string(raw), "[") {
		t.Fatalf("attention = %d %s", code, raw)
	}
	if code, _, _ := newClientFor(e.client).raw("GET", "/api/alerts/attention", "", nil); code != 401 {
		t.Fatalf("attention without a session = %d", code)
	}
	streams[0].resp.Body.Close()
	eventually(t, "a stream is free again", func() bool {
		_, data, _ := e.do("GET", "/api/session", "", nil)
		return data["streamsFull"] == nil
	})
}

// What needs attention is known as soon as the server runs, also when nothing ever changes: a page that opens on
// a server without apps is told "nothing" at once, and need not wait for the first tick to say so.
func TestAttentionIsKnownWithoutAnyChange(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	e.openEvents(timeout(t)).until("attention", func(data string) bool { return strings.TrimSpace(data) == "[]" })
}

// Adding alongside keeps the alert rules set on this server, and a server that never had any set takes the
// file's; the preview says which it will be.
func TestAddAlongsideRestoresRulesWhereNoneAreSet(t *testing.T) {
	src := newServicesEnv(t, withAlerts(t))
	src.create("App")
	r := alert.DefaultRules()
	r.DownMinutes, r.Link = 1, "https://observarr.example"
	body, _ := json.Marshal(r)
	if code, _ := src.send("PUT", "/api/alerts/rules", string(body)); code != 200 {
		t.Fatal("setting the rules failed")
	}
	file := src.exported(withKeys)

	fresh := newServicesEnv(t, withAlerts(t))
	if code, rep, msg := fresh.importFile(file, passphrase, "preview"); code != 200 || rep.Rules != "restored" {
		t.Fatalf("preview on a fresh server = %d %+v %s", code, rep, msg)
	}
	if code, rep, msg := fresh.importFile(file, passphrase, "add"); code != 200 || rep.Rules != "restored" {
		t.Fatalf("add on a fresh server = %d %+v %s", code, rep, msg)
	}
	if got := fresh.srv.Alerts.Rules(); got.DownMinutes != 1 || got.Link != "https://observarr.example" {
		t.Fatalf("rules on the fresh server = %+v, want the file's", got)
	}

	own := newServicesEnv(t, withAlerts(t))
	mine := alert.DefaultRules()
	mine.DownMinutes = 7
	body, _ = json.Marshal(mine)
	own.send("PUT", "/api/alerts/rules", string(body))
	if code, rep, msg := own.importFile(file, passphrase, "preview"); code != 200 || rep.Rules != "kept" {
		t.Fatalf("preview where rules are set = %d %+v %s", code, rep, msg)
	}
	if code, _, msg := own.importFile(file, passphrase, "add"); code != 200 {
		t.Fatalf("add where rules are set = %d %s", code, msg)
	}
	if got := own.srv.Alerts.Rules(); got.DownMinutes != 7 {
		t.Fatalf("rules where they were set = %+v, want them kept", got)
	}
}
