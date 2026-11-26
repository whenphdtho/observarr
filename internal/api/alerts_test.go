package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"observarr/internal/alert"
)

// withAlerts runs an alert service on the server's store, box and hub.
func withAlerts(t *testing.T) func(*Server) {
	return func(s *Server) {
		ctx, cancel := context.WithCancel(context.Background())
		s.Alerts = alert.New(alert.Config{Store: s.Store, Box: s.Box, Hub: s.Hub, Deps: alert.Deps{HTTP: alert.NewHTTPClient()},
			Log: s.Log, Every: time.Hour, Backoff: time.Millisecond})
		if err := s.Alerts.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); s.Alerts.Wait() })
	}
}

// hook is a webhook destination that counts what it receives.
func hook(t *testing.T) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		n.Add(1)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// addHook saves a webhook channel whose address holds testKey and returns its id.
func (e *servicesEnv) addHook(url, name string) int64 {
	e.t.Helper()
	code, data := e.send("POST", "/api/alerts/channels",
		fmt.Sprintf(`{"type":"webhook","name":%q,"secrets":{"url":%q,"secret":%q}}`, name, url+"/"+testKey, testKey))
	if code != 201 {
		e.t.Fatalf("add channel = %d %v", code, data)
	}
	return int64(data["id"].(float64))
}

func channelPath(id int64) string { return "/api/alerts/channels/" + strconv.FormatInt(id, 10) }

func TestAlertChannelIsEnabledOnlyAfterATest(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	srv, received := hook(t)
	id := e.addHook(srv.URL, "Hook")

	code, raw, _ := e.raw("GET", "/api/alerts/channels", "", nil)
	var list []channelView
	if err := json.Unmarshal(raw, &list); code != 200 || err != nil || len(list) != 1 {
		t.Fatalf("list = %d %s", code, raw)
	}
	if c := list[0]; c.Enabled || c.Tested || !c.HasSecret["url"] || !c.HasSecret["secret"] || c.Target != srv.URL ||
		len(c.Rules) != len(alert.AllRules) {
		t.Fatalf("channel = %+v", c)
	}
	if code, data := e.send("PUT", channelPath(id), `{"enabled":true}`); code != 409 {
		t.Fatalf("enable before a test = %d %v", code, data)
	}
	code, data := e.send("POST", channelPath(id)+"/test", "")
	if code != 200 || data["ok"] != true || data["note"] != "delivered" || received.Load() != 1 {
		t.Fatalf("test = %d %v", code, data)
	}
	if code, data := e.send("PUT", channelPath(id), `{"enabled":true,"rules":["down"]}`); code != 200 ||
		data["enabled"] != true {
		t.Fatalf("enable after a test = %d %v", code, data)
	}
	// A new address needs a new test.
	code, data = e.send("PUT", channelPath(id), fmt.Sprintf(`{"secrets":{"url":%q}}`, srv.URL+"/other"))
	if code != 200 || data["enabled"] != false || data["tested"] != false || data["hasSecret"].(map[string]any)["secret"] != true {
		t.Fatalf("change address = %d %v", code, data)
	}
	// Removing the optional secret.
	if code, data = e.send("PUT", channelPath(id), `{"secrets":{"secret":""}}`); code != 200 ||
		data["hasSecret"].(map[string]any)["secret"] != false {
		t.Fatalf("remove secret = %d %v", code, data)
	}
	if code, _ := e.send("DELETE", channelPath(id), ""); code != 204 {
		t.Fatalf("delete = %d", code)
	}
	for _, want := range []string{"added alert channel: Hook (webhook)", "tested alert channel: Hook: delivered",
		"changed alert channel: Hook: enabled", "changed alert channel: Hook: settings changed, disabled until tested",
		"removed alert channel: Hook"} {
		if !e.hasAudit(want) {
			t.Errorf("audit lacks %q: %v", want, e.auditRows())
		}
	}
	for _, row := range e.auditRows() {
		if strings.Contains(row, testKey) {
			t.Fatalf("a secret reached the audit: %s", row)
		}
	}
	if log := e.logs.String(); strings.Contains(log, srv.URL+"/"+testKey) {
		t.Fatal("a secret address reached the log")
	}
}

func TestAlertChannelInputIsChecked(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	for _, body := range []string{
		`{"type":"pager","name":"x"}`,
		`{"type":"webhook","name":"","secrets":{"url":"http://h.example"}}`,
		`{"type":"webhook","name":"x","secrets":{"url":"http://u:p@h.example"}}`,
		`{"type":"webhook","name":"x","secrets":{"url":"http://h.example"},"rules":["reboots"]}`,
		`{"type":"ntfy","name":"x","settings":{"topic":"a","host":"m"}}`,
		`{"type":"email","name":"x","settings":{"host":"m","security":"none","from":"a@b.c","to":"d@e.f"}}`,
	} {
		if code, data := e.send("POST", "/api/alerts/channels", body); code != 400 {
			t.Errorf("%s = %d %v, want 400", body, code, data)
		}
	}
	ok := `{"type":"ntfy","name":"Phone","settings":{"topic":"alerts"},"secrets":{"token":"` + testKey + `"}}`
	if code, _ := e.send("POST", "/api/alerts/channels", ok); code != 201 {
		t.Fatal("a valid channel was refused")
	}
	if code, _ := e.send("POST", "/api/alerts/channels", ok); code != 409 {
		t.Fatal("a second channel with the same name was accepted")
	}
	// The token goes only to the server it was entered for.
	code, data := e.send("PUT", channelPath(1), `{"settings":{"server":"https://ntfy.example.org","topic":"alerts"}}`)
	if code != 400 || !strings.Contains(data["error"].(string), "enter the token again") {
		t.Fatalf("moving the token = %d %v", code, data)
	}
	if code, data = e.send("PUT", channelPath(1),
		`{"settings":{"server":"https://ntfy.example.org","topic":"alerts"},"secrets":{"token":"new-token"}}`); code != 200 {
		t.Fatalf("moving with a new token = %d %v", code, data)
	}
}

func TestAlertRoutesNeedSessionAndCSRF(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	anon := newClientFor(e.client)
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/alerts/channels", ""}, {"POST", "/api/alerts/channels", `{"type":"ntfy","name":"x","settings":{"topic":"a"}}`},
		{"PUT", "/api/alerts/channels/1", `{"name":"y"}`}, {"DELETE", "/api/alerts/channels/1", ""},
		{"POST", "/api/alerts/channels/1/test", ""}, {"GET", "/api/alerts/rules", ""},
		{"PUT", "/api/alerts/rules", `{}`}, {"GET", "/api/alerts/log", ""}, {"GET", "/api/alerts/firing", ""},
	} {
		if code, _, _ := anon.raw(rt.method, rt.path, rt.body, nil); code != 401 {
			t.Errorf("%s %s without a session = %d", rt.method, rt.path, code)
		}
		if rt.method != "GET" {
			if code, _, _ := e.raw(rt.method, rt.path, rt.body, nil); code != 403 {
				t.Errorf("%s %s without CSRF = %d", rt.method, rt.path, code)
			}
		}
	}
	if chans, _ := e.srv.Store.AlertChannels(context.Background()); len(chans) != 0 {
		t.Fatal("a refused request saved a channel")
	}
}

func TestAlertRules(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	code, data, _ := e.do("GET", "/api/alerts/rules", "", nil)
	if code != 200 || data["downMinutes"] != 3.0 || data["enabled"].(map[string]any)["requests"] != false {
		t.Fatalf("rules = %d %v", code, data)
	}
	if code, _ := e.send("PUT", "/api/alerts/rules", `{"enabled":{},"downMinutes":3,"fullPercent":10,"failedSignIns":10,
		"repeatHours":0,"perHour":30,"quiet":{"on":false,"from":"22:00","to":"07:00"},"link":""}`); code != 400 {
		t.Fatalf("invalid rules = %d", code)
	}
	r := alert.DefaultRules()
	r.DownMinutes, r.Quiet.On, r.Link = 10, true, "https://observarr.example"
	body, _ := json.Marshal(r)
	if code, data := e.send("PUT", "/api/alerts/rules", string(body)); code != 200 {
		t.Fatalf("set rules = %d %v", code, data)
	}
	if got := e.srv.Alerts.Rules(); got.DownMinutes != 10 || !got.Quiet.On {
		t.Fatalf("rules in force = %+v", got)
	}
	if !e.hasAudit("changed alert rules: ") {
		t.Fatalf("audit = %v", e.auditRows())
	}
	if code, raw, _ := e.raw("GET", "/api/alerts/firing", "", nil); code != 200 || string(raw) != "[]\n" {
		t.Fatalf("firing = %d %s", code, raw)
	}
	if code, _, _ := e.raw("GET", "/api/alerts/log?limit=0", "", nil); code != 400 {
		t.Fatal("limit 0 accepted")
	}
}

func TestSettingsRoundTripWithAlertChannels(t *testing.T) {
	src := newServicesEnv(t, withAlerts(t))
	srv, _ := hook(t)
	id := src.addHook(srv.URL, "Hook")
	src.send("POST", channelPath(id)+"/test", "")
	src.send("PUT", channelPath(id), `{"enabled":true}`)
	if code, _ := src.send("POST", "/api/alerts/channels", `{"type":"ntfy","name":"Phone","settings":{"topic":"t"}}`); code != 201 {
		t.Fatal("ntfy channel refused")
	}
	file := src.exported(withKeys)
	if strings.Contains(string(file), testKey) {
		t.Fatal("a channel secret is in the file in plain text")
	}
	if !src.hasAudit("exported settings: services: 2, keys encrypted: 1, alert channels: 2") {
		t.Fatalf("audit = %v", src.auditRows())
	}

	dst := newServicesEnv(t, withAlerts(t))
	code, rep, msg := dst.importFile(file, passphrase, "replace")
	if code != 200 || len(rep.Channels) != 2 || rep.Channels[0].Name != "Hook" || rep.Channels[0].Secrets != "included" ||
		!rep.Channels[0].Enabled || rep.Channels[1].Secrets != "none" || rep.Channels[1].Enabled {
		t.Fatalf("import = %d %+v %s", code, rep, msg)
	}
	chans, _ := dst.srv.Store.AlertChannels(context.Background())
	sec, err := alert.OpenSecrets(dst.box, chans[0].ID, chans[0].Secrets)
	if err != nil || sec["url"] != srv.URL+"/"+testKey || sec["secret"] != testKey || !chans[0].Enabled {
		t.Fatalf("imported channel = %+v, %v", chans[0], err)
	}

	// Without keys, the channel comes back without its secrets, disabled, to be entered again.
	bare := newServicesEnv(t, withAlerts(t))
	body, _ := json.Marshal(map[string]any{"file": string(file), "withoutKeys": true, "mode": "add"})
	code, raw, _ := bare.raw("POST", "/api/settings/import", string(body), map[string]string{"X-CSRF-Token": bare.csrf})
	var rep2 importReport
	if json.Unmarshal(raw, &rep2); code != 200 || rep2.Channels[0].Secrets != "missing" || rep2.Channels[0].Enabled {
		t.Fatalf("import without keys = %d %s", code, raw)
	}
	_, raw, _ = bare.raw("GET", "/api/alerts/channels", "", nil)
	var list []channelView
	json.Unmarshal(raw, &list)
	if list[0].Enabled || list[0].HasSecret["url"] {
		t.Fatalf("channel without its secrets = %+v", list[0])
	}

	// Adding again skips the names that are taken.
	code, rep, _ = dst.importFile(file, passphrase, "add")
	if code != 200 || len(rep.Channels) != 0 || len(rep.Skipped) != 4 {
		t.Fatalf("second import = %d %+v", code, rep)
	}
}
