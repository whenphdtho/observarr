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
	"observarr/internal/backup"
	"observarr/internal/shape"
	"observarr/internal/state"
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
	// A new address needs its signing secret again, and a new test.
	code, data = e.send("PUT", channelPath(id), fmt.Sprintf(`{"secrets":{"url":%q,"secret":%q}}`, srv.URL+"/other", testKey))
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
		`{"type":"ntfy","name":"x","settings":{"host":"m"},"secrets":{"topic":"a"}}`,
		`{"type":"ntfy","name":"x","settings":{"topic":"a"}}`,
		`{"type":"email","name":"x","settings":{"host":"m","security":"none","from":"a@b.c","to":"d@e.f"}}`,
	} {
		if code, data := e.send("POST", "/api/alerts/channels", body); code != 400 {
			t.Errorf("%s = %d %v, want 400", body, code, data)
		}
	}
	ok := `{"type":"ntfy","name":"Phone","secrets":{"topic":"alerts","token":"` + testKey + `"}}`
	if code, _ := e.send("POST", "/api/alerts/channels", ok); code != 201 {
		t.Fatal("a valid channel was refused")
	}
	if code, _ := e.send("POST", "/api/alerts/channels", ok); code != 409 {
		t.Fatal("a second channel with the same name was accepted")
	}
	// The topic is a secret: where the channel sends does not show it.
	_, raw, _ := e.raw("GET", "/api/alerts/channels", "", nil)
	if !strings.Contains(string(raw), `"target":"ntfy.sh/…"`) || strings.Contains(string(raw), "alerts\"") {
		t.Fatalf("channels = %s", raw)
	}
	// The same server written another way is the same server; the secrets go only to the server they were
	// entered for, so another needs each of them again.
	if code, data := e.send("PUT", channelPath(1), `{"settings":{"server":"HTTPS://NTFY.sh:443/"}}`); code != 200 {
		t.Fatalf("the same server = %d %v", code, data)
	}
	code, data := e.send("PUT", channelPath(1), `{"settings":{"server":"https://ntfy.example.org"}}`)
	if code != 400 || !strings.Contains(data["error"].(string), "again when changing the server") {
		t.Fatalf("moving the secrets = %d %v", code, data)
	}
	if code, data = e.send("PUT", channelPath(1), `{"settings":{"server":"https://ntfy.example.org"},`+
		`"secrets":{"topic":"alerts","token":"new-token-1"}}`); code != 200 {
		t.Fatalf("moving with the secrets entered again = %d %v", code, data)
	}
}

// A webhook's signing secret goes only to the address it was entered for.
func TestWebhookAddressChangeNeedsTheSecretAgain(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	id := e.addHook("https://hook.example", "Hook")
	code, data := e.send("PUT", channelPath(id), `{"secrets":{"url":"https://elsewhere.example/x"}}`)
	if code != 400 || !strings.Contains(data["error"].(string), "enter the secret again") {
		t.Fatalf("moving the signing secret = %d %v", code, data)
	}
	if code, data = e.send("PUT", channelPath(id),
		`{"secrets":{"url":"https://elsewhere.example/x","secret":"another-signing-secret"}}`); code != 200 {
		t.Fatalf("with the secret entered again = %d %v", code, data)
	}
	if code, data = e.send("PUT", channelPath(id), `{"secrets":{"url":"https://elsewhere.example/x"}}`); code != 200 {
		t.Fatalf("the same address = %d %v", code, data)
	}
}

// One test runs per channel at a time, and a session sends only a few a minute.
func TestChannelTestsAreLimited(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	srv, _ := hook(t)
	slowID, fastID := e.addHook(slow.URL, "Slow"), e.addHook(srv.URL, "Fast")
	first := make(chan int)
	go func() {
		code, _ := e.send("POST", channelPath(slowID)+"/test", "")
		first <- code
	}()
	eventually(t, "the first test is under way", func() bool {
		e.srv.alertTests.mu.Lock()
		defer e.srv.alertTests.mu.Unlock()
		return e.srv.alertTests.running[slowID]
	})
	if code, data := e.send("POST", channelPath(slowID)+"/test", ""); code != 429 {
		t.Fatalf("a second test of the same channel = %d %v", code, data)
	}
	close(release)
	if code := <-first; code != 200 {
		t.Fatalf("first test = %d", code)
	}
	codes := []int{}
	for range testsPerMinute {
		code, _ := e.send("POST", channelPath(fastID)+"/test", "")
		codes = append(codes, code)
	}
	if codes[testsPerMinute-2] != 200 || codes[testsPerMinute-1] != 429 {
		t.Fatalf("tests in a minute = %v", codes)
	}
}

func TestAlertRoutesNeedSessionAndCSRF(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	anon := newClientFor(e.client)
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/alerts/channels", ""}, {"POST", "/api/alerts/channels", `{"type":"ntfy","name":"x","settings":{"topic":"a"}}`},
		{"PUT", "/api/alerts/channels/1", `{"name":"y"}`}, {"DELETE", "/api/alerts/channels/1", ""},
		{"POST", "/api/alerts/channels/1/test", ""}, {"GET", "/api/alerts/rules", ""},
		{"PUT", "/api/alerts/rules", `{}`}, {"GET", "/api/alerts/log", ""},
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
	if code, _ := src.send("POST", "/api/alerts/channels", `{"type":"ntfy","name":"Phone","secrets":{"topic":"t"}}`); code != 201 {
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
	// Every channel comes back switched off, to be tested here first, even one that brought its secrets.
	if code != 200 || len(rep.Channels) != 2 || rep.Channels[0].Name != "Hook" || rep.Channels[0].Secrets != "included" ||
		rep.Channels[0].Note != "send a test to switch it on" || rep.Channels[1].Secrets != "included" {
		t.Fatalf("import = %d %+v %s", code, rep, msg)
	}
	chans, _ := dst.srv.Store.AlertChannels(context.Background())
	sec, err := alert.OpenSecrets(dst.box, chans[0].ID, chans[0].Secrets)
	if err != nil || sec["url"] != srv.URL+"/"+testKey || sec["secret"] != testKey || chans[0].Enabled || chans[0].Tested {
		t.Fatalf("imported channel = %+v, %v", chans[0], err)
	}
	if sec, _ := alert.OpenSecrets(dst.box, chans[1].ID, chans[1].Secrets); sec["topic"] != "t" {
		t.Fatalf("imported ntfy secrets = %v", sec)
	}

	// Without keys, the channel comes back without its secrets, disabled, to be entered again.
	bare := newServicesEnv(t, withAlerts(t))
	body, _ := json.Marshal(map[string]any{"file": string(file), "withoutKeys": true, "mode": "add"})
	code, raw, _ := bare.raw("POST", "/api/settings/import", string(body), map[string]string{"X-CSRF-Token": bare.csrf})
	var rep2 importReport
	if json.Unmarshal(raw, &rep2); code != 200 || rep2.Channels[0].Secrets != "missing" || rep2.Channels[1].Secrets != "missing" ||
		!strings.Contains(rep2.Channels[1].Note, "enter its secrets again") {
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

// sealedFile is a settings file with one webhook channel that was enabled, listing secrets, with keys holding
// sec for it.
func sealedFile(t *testing.T, listed []string, sec map[string]string) []byte {
	t.Helper()
	f := backup.File{Format: backup.Format, Version: backup.Version, Services: []backup.Service{}, Dismissed: []string{},
		Alerts: &backup.Alerts{Rules: json.RawMessage(`{"enabled":{"down":true},"downMinutes":3,"fullPercent":90,` +
			`"failedSignIns":10,"repeatHours":0,"perHour":30,"quiet":{"on":false,"from":"22:00","to":"07:00"},"link":""}`),
			Channels: []backup.Channel{{ID: 1, Type: "webhook", Name: "Hook", Settings: json.RawMessage(`{}`),
				Rules: []string{"down"}, Enabled: true, Secrets: listed}}}}
	if sec != nil {
		if err := backup.Seal(&f, backup.Keys{Channels: map[int64]map[string]string{1: sec}}, passphrase); err != nil {
			t.Fatal(err)
		}
	}
	body, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// A settings file is never trusted to send alerts: an imported channel is off until a test from here got
// through, and the secrets it brings are exactly the ones it lists, checked as if entered by hand.
func TestImportedChannelsAreOffAndChecked(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	// A file without keys whose email channel was on: imported, but off.
	bare := []byte(`{"format":"observarr-settings","version":2,"services":[],"dismissed":[],"secrets":null,` +
		`"alerts":{"rules":{"enabled":{"down":true},"downMinutes":3,"fullPercent":90,"failedSignIns":10,"repeatHours":0,` +
		`"perHour":30,"quiet":{"on":false,"from":"22:00","to":"07:00"},"link":""},"channels":[{"id":1,"type":"email",` +
		`"name":"Mail","settings":{"host":"mail.example.net","security":"tls","from":"a@example.net",` +
		`"to":"b@example.net"},"rules":["down"],"enabled":true,"secrets":[]}]}}`)
	if code, rep, msg := e.importFile(bare, "", "add"); code != 200 || rep.Channels[0].Secrets != "none" ||
		rep.Channels[0].Note != "send a test to switch it on" {
		t.Fatalf("import without keys = %d %+v %s", code, rep, msg)
	}
	if chans, _ := e.srv.Store.AlertChannels(context.Background()); len(chans) != 1 || chans[0].Enabled || chans[0].Tested {
		t.Fatalf("imported channel = %+v", chans)
	}
	good := sealedFile(t, []string{"url"}, map[string]string{"url": "https://hook.example/x"})
	code, rep, msg := e.importFile(good, passphrase, "add")
	if code != 200 || rep.Channels[0].Secrets != "included" || rep.Channels[0].Note != "send a test to switch it on" {
		t.Fatalf("import = %d %+v %s", code, rep, msg)
	}
	chans, _ := e.srv.Store.AlertChannels(context.Background())
	if len(chans) != 2 || chans[0].Name != "Hook" || chans[0].Enabled || chans[0].Tested {
		t.Fatalf("imported channels = %+v", chans)
	}
	for name, file := range map[string][]byte{
		"unlisted secret": sealedFile(t, []string{"url"}, map[string]string{"url": "https://hook.example/x", "token": "x"}),
		"bad address":     sealedFile(t, []string{"url"}, map[string]string{"url": "http://u:p@hook.example/x"}),
		"short secret": sealedFile(t, []string{"url", "secret"},
			map[string]string{"url": "https://hook.example/x", "secret": "short"}),
		"control character": sealedFile(t, []string{"url"}, map[string]string{"url": "https://hook.example/\x01"}),
	} {
		if code, _, msg := e.importFile(file, passphrase, "add"); code != 400 {
			t.Errorf("%s: import = %d %s", name, code, msg)
		}
	}
}

// The live stream carries what needs attention, every rule's conditions with whether each fires.
func TestEventsCarryAttention(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	const id = 99
	e.srv.Hub.Put(state.ServiceState{ID: id, Name: "App"})
	s := e.openEvents(timeout(t))
	e.srv.Hub.Record(id, shape.KindHealth, shape.Reading{Error: "down for a test", At: time.Now()})
	s.until("attention", func(data string) bool {
		var items []alert.Item
		return json.Unmarshal([]byte(data), &items) == nil && len(items) == 1 && items[0].Rule == alert.RuleDown &&
			items[0].Severity == alert.Critical && items[0].Service == id && !items[0].Firing
	})
}
