package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"observarr/internal/alert"
	"observarr/internal/shape"
	"observarr/internal/state"
)

func TestMuteAndUnmute(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	const id = 99
	e.srv.Hub.Put(state.ServiceState{ID: id, Name: "App"})
	s := e.openEvents(timeout(t))
	e.srv.Hub.Record(id, shape.KindHealth, shape.Reading{Error: "down for a test", At: time.Now()})
	muted := func(want bool) func(string) bool {
		return func(data string) bool {
			var items []alert.Item
			return json.Unmarshal([]byte(data), &items) == nil && len(items) == 1 && items[0].Key == "down/99" &&
				items[0].MutedUntil.IsZero() != want
		}
	}
	s.until("attention", muted(false))

	for _, bad := range []string{`{"key":"down/99","minutes":5}`, `{"key":"down/99","minutes":0}`, `{"key":"down/99"}`} {
		if code, data := e.send("POST", "/api/alerts/mutes", bad); code != 400 || data["error"] != "minutes: 60, 480 or 1440" {
			t.Errorf("%s = %d %v", bad, code, data)
		}
	}
	if code, _ := e.send("POST", "/api/alerts/mutes", `{"key":"down/1","minutes":60}`); code != 404 {
		t.Errorf("muting a key nothing has = %d", code)
	}
	code, data := e.send("POST", "/api/alerts/mutes", `{"key":"down/99","minutes":480}`)
	until, err := time.Parse(time.RFC3339, data["mutedUntil"].(string))
	if code != 200 || err != nil || time.Until(until) < 7*time.Hour || time.Until(until) > 8*time.Hour {
		t.Fatalf("mute = %d %v", code, data)
	}
	// The live stream says so at once.
	s.until("attention", muted(true))
	if mutes, _ := e.srv.Store.AlertMutes(context.Background()); len(mutes) != 1 || mutes[0].CreatedBy != "admin" {
		t.Fatalf("saved mutes = %+v", mutes)
	}

	// Mutes are this server's passing state: they are in no settings file.
	if file := string(e.exported(withKeys)); strings.Contains(file, "mute") || strings.Contains(file, "down/99") {
		t.Fatal("a mute is in the settings file")
	}

	// The key holds a slash: it is one path value, escaped or not.
	if code, _ := e.send("DELETE", "/api/alerts/mutes/down%2F99", ""); code != 204 {
		t.Fatalf("unmute = %d", code)
	}
	s.until("attention", muted(false))
	if code, _ := e.send("DELETE", "/api/alerts/mutes/down/99", ""); code != 204 {
		t.Fatalf("unmuting what is not muted = %d", code)
	}
	for _, want := range []string{"muted alert: down/99 for 480 min", "unmuted alert: down/99"} {
		if !e.hasAudit(want) {
			t.Errorf("audit lacks %q: %v", want, e.auditRows())
		}
	}
}

func TestMuteRoutesNeedSessionAndCSRF(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	anon := newClientFor(e.client)
	for _, rt := range []struct{ method, path, body string }{
		{"POST", "/api/alerts/mutes", `{"key":"down/1","minutes":60}`}, {"DELETE", "/api/alerts/mutes/down/1", ""},
	} {
		if code, _, _ := anon.raw(rt.method, rt.path, rt.body, nil); code != 401 {
			t.Errorf("%s %s without a session = %d", rt.method, rt.path, code)
		}
		if code, _, _ := e.raw(rt.method, rt.path, rt.body, nil); code != 403 {
			t.Errorf("%s %s without CSRF = %d", rt.method, rt.path, code)
		}
	}
}
