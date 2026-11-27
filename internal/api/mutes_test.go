package api

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"observarr/internal/alert"
	"observarr/internal/collect"
	"observarr/internal/shape"
	"observarr/internal/state"
	"observarr/internal/store"
	"observarr/internal/system"
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

	if code, _ := e.send("DELETE", "/api/alerts/mutes?key=down%2F99", ""); code != 204 {
		t.Fatalf("unmute = %d", code)
	}
	s.until("attention", muted(false))
	for _, want := range []string{"muted alert: down/99 for 480 min", "unmuted alert: down/99"} {
		if !e.hasAudit(want) {
			t.Errorf("audit lacks %q: %v", want, e.auditRows())
		}
	}
	// Nothing is muted now: a 404, and nothing more in the audit.
	rows := len(e.auditRows())
	for _, path := range []string{"/api/alerts/mutes?key=down%2F99", "/api/alerts/mutes?key=never", "/api/alerts/mutes", "/api/alerts/mutes/down/99"} {
		if code, _ := e.send("DELETE", path, ""); code != 404 {
			t.Errorf("DELETE %s = %d, want 404", path, code)
		}
	}
	if got := len(e.auditRows()); got != rows {
		t.Errorf("unmuting nothing was audited: %v", e.auditRows())
	}
}

// A key is free text: one ends in "//" (the disk "/"), one in "#1" (the second of two alike downloads). Both mute
// and unmute, as a query value.
func TestMuteKeysWithSlashesAndHashes(t *testing.T) {
	e := newServicesEnv(t, func(s *Server) {
		ctx, cancel := context.WithCancel(context.Background())
		s.Alerts = alert.New(alert.Config{Store: s.Store, Box: s.Box, Hub: s.Hub, Deps: alert.Deps{HTTP: alert.NewHTTPClient()},
			Log: s.Log, Every: time.Hour, Backoff: time.Millisecond, System: func() system.Snapshot {
				return system.Snapshot{At: time.Now(), Notices: []string{system.NoticeDocker},
					Host: &system.HostView{Disks: []collect.Disk{{Path: "/", Total: 1000, Free: 20, Avail: 20}}}}
			}})
		if err := s.Alerts.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cancel(); s.Alerts.Wait() })
	})
	const id = 99
	e.srv.Hub.Put(state.ServiceState{ID: id, Name: "App", Up: true, Kinds: []shape.Kind{shape.KindHealth, shape.KindQueue}})
	s := e.openEvents(timeout(t))
	e.srv.Hub.Record(id, shape.KindHealth, shape.Reading{Data: shape.Health{Warnings: []shape.Warning{}}, At: time.Now()})
	stuck := shape.QueueItem{Title: "Sintel", Status: "warning", Problem: "stalled"}
	e.srv.Hub.Record(id, shape.KindQueue, shape.Reading{Data: shape.Queue{Total: 2, Items: []shape.QueueItem{stuck, stuck}}, At: time.Now()})
	var keys []string
	s.until("attention", func(data string) bool {
		var items []alert.Item
		if json.Unmarshal([]byte(data), &items) != nil || len(items) != 3 {
			return false
		}
		keys = keys[:0]
		for _, it := range items {
			keys = append(keys, it.Key)
		}
		return true
	})
	var disk, second string
	for _, k := range keys {
		if k == "disk//" {
			disk = k
		}
		if strings.HasSuffix(k, "#1") {
			second = k
		}
	}
	if disk == "" || second == "" {
		t.Fatalf("keys = %q, want the disk \"/\" and a second alike download", keys)
	}
	for _, key := range []string{disk, second} {
		body, _ := json.Marshal(map[string]any{"key": key, "minutes": 60})
		if code, data := e.send("POST", "/api/alerts/mutes", string(body)); code != 200 {
			t.Fatalf("mute %q = %d %v", key, code, data)
		}
		if code, _ := e.send("DELETE", "/api/alerts/mutes?key="+url.QueryEscape(key), ""); code != 204 {
			t.Fatalf("unmute %q = %d", key, code)
		}
		if !e.hasAudit("unmuted alert: " + key) {
			t.Errorf("audit lacks the unmute of %q: %v", key, e.auditRows())
		}
	}
	if mutes, _ := e.srv.Store.AlertMutes(context.Background()); len(mutes) != 0 {
		t.Fatalf("mutes left: %+v", mutes)
	}
}

func TestMutesAreBounded(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	const id = 99
	e.srv.Hub.Put(state.ServiceState{ID: id, Name: "App"})
	s := e.openEvents(timeout(t))
	e.srv.Hub.Record(id, shape.KindHealth, shape.Reading{Error: "down for a test", At: time.Now()})
	s.until("attention", func(data string) bool { return strings.Contains(data, `"down/99"`) })
	for i := range store.MaxAlertMutes {
		m := store.AlertMute{Key: "k/" + strconv.Itoa(i), Until: time.Now().Add(time.Hour), Severity: "warning", CreatedBy: "admin"}
		if err := e.srv.Store.SetAlertMute(context.Background(), m, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	code, data := e.send("POST", "/api/alerts/mutes", `{"key":"down/99","minutes":60}`)
	if code != 409 || data["error"] != "at most 500 mutes at once: unmute some first" {
		t.Fatalf("one mute too many = %d %v", code, data)
	}
	if e.hasAudit("muted alert: down/99 for 60 min") {
		t.Fatal("a refused mute was audited")
	}
}

func TestMuteRoutesNeedSessionAndCSRF(t *testing.T) {
	e := newServicesEnv(t, withAlerts(t))
	anon := newClientFor(e.client)
	for _, rt := range []struct{ method, path, body string }{
		{"POST", "/api/alerts/mutes", `{"key":"down/1","minutes":60}`}, {"DELETE", "/api/alerts/mutes?key=down%2F1", ""},
	} {
		if code, _, _ := anon.raw(rt.method, rt.path, rt.body, nil); code != 401 {
			t.Errorf("%s %s without a session = %d", rt.method, rt.path, code)
		}
		if code, _, _ := e.raw(rt.method, rt.path, rt.body, nil); code != 403 {
			t.Errorf("%s %s without CSRF = %d", rt.method, rt.path, code)
		}
	}
}
