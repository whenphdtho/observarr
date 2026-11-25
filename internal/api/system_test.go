package api

import (
	"cmp"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"observarr/internal/system"
)

func TestSystemRoutesNeedSession(t *testing.T) {
	e := newServicesEnv(t)
	anon := newClientFor(e.client)
	for _, path := range []string{"/api/system", "/api/system/history", "/api/network", "/api/network?range=24h"} {
		if code, _, _ := anon.raw("GET", path, "", nil); code != 401 {
			t.Errorf("GET %s without a session = %d, want 401", path, code)
		}
	}
}

func TestSystemWithoutSourcesGivesNotices(t *testing.T) {
	e := newServicesEnv(t)
	var snap map[string]any
	eventually(t, "the first sample", func() bool {
		code, data, _ := e.do("GET", "/api/system", "", nil)
		snap = data
		return code == 200 && len(data["notices"].([]any)) > 0
	})
	notices := snap["notices"].([]any)
	if snap["host"] != nil || len(snap["containers"].([]any)) != 0 || len(notices) != 2 ||
		!strings.Contains(notices[0].(string), "OBSERVARR_HOST_PROC") ||
		!strings.Contains(notices[1].(string), "OBSERVARR_DOCKER_HOST") {
		t.Fatalf("snapshot = %v", snap)
	}
	code, raw, _ := e.raw("GET", "/api/system/history", "", nil)
	if code != 200 || strings.TrimSpace(string(raw)) != "[]" {
		t.Fatalf("history = %d %s", code, raw)
	}
}

func TestSystemContainers(t *testing.T) {
	e := newServicesEnv(t, withDocker(&fakeCollector{}))
	eventually(t, "the first sample", func() bool { return len(e.srv.System.Snapshot().Containers) == 1 })
	code, raw, _ := e.raw("GET", "/api/system", "", nil)
	var snap struct{ Containers []map[string]any }
	if code != 200 || json.Unmarshal(raw, &snap) != nil {
		t.Fatalf("GET /api/system = %d %s", code, raw)
	}
	c := snap.Containers[0]
	for _, key := range []string{"id", "name", "image", "state", "health", "restartCount", "networkMode", "ports",
		"labels", "cpuPercent", "memBytes", "memLimit", "blkReadRate", "blkWriteRate", "netRxRate", "netTxRate"} {
		if _, ok := c[key]; !ok {
			t.Errorf("container lacks %q: %s", key, raw)
		}
	}
	if c["name"] != "app" || c["memBytes"] != float64(123<<20) || strings.Contains(strings.ToLower(string(raw)), "pid") {
		t.Fatalf("container = %v", c)
	}
}

func TestNetwork(t *testing.T) {
	e := newServicesEnv(t, withDocker(&fakeCollector{}))
	eventually(t, "the first sample", func() bool { v, _ := e.srv.System.Network(context.Background(), "5m"); return len(v.Containers) == 1 })
	if code, data := e.send("POST", "/api/services", `{"type":"keyless","name":"Linked","url":"`+e.appURL+
		`/open","container":"app"}`); code != 201 {
		t.Fatalf("create = %d %v", code, data)
	}
	for _, rng := range []string{"2y", "1h", "24H"} {
		if code, _, _ := e.raw("GET", "/api/network?range="+rng, "", nil); code != 400 {
			t.Fatalf("network range %q = %d, want 400", rng, code)
		}
		if code, _, _ := e.raw("GET", "/api/system/history?range="+rng, "", nil); code != 400 {
			t.Fatalf("history range %q = %d, want 400", rng, code)
		}
	}
	steps := map[string][2]int{"24h": {60, 1440}, "7d": {3600, 168}, "30d": {3600, 720}, "1y": {86400, 366}}
	today := time.Now().Format(time.DateOnly)
	for _, rng := range []string{"", "5m", "24h", "7d", "30d", "1y"} {
		code, raw, _ := e.raw("GET", "/api/network?range="+rng, "", nil)
		var v struct {
			Range      string
			Step       int
			Times      []int64
			Host       map[string]any
			Budget     any
			Containers []map[string]any
			Previous   any
			Daily      []struct {
				Day     string
				In, Out *uint64
			}
		}
		if code != 200 || json.Unmarshal(raw, &v) != nil {
			t.Fatalf("GET /api/network?range=%s = %d %s", rng, code, raw)
		}
		if v.Range != cmp.Or(rng, "5m") || v.Times == nil || v.Host == nil || v.Budget != nil ||
			len(v.Containers) != 1 || v.Containers[0]["name"] != "app" || v.Containers[0]["service"] != "Linked" {
			if !(len(v.Containers) == 0 && steps[rng][0] > 60) { // the longer ranges come from the history, kept nowhere here
				t.Fatalf("range %q: %s", rng, raw)
			}
		}
		if want, ok := steps[rng]; ok && (v.Step != want[0] || len(v.Times) != want[1]) {
			t.Fatalf("range %q: step %d, %d times; want %v", rng, v.Step, len(v.Times), want)
		}
		if !strings.Contains(string(raw), `"previous":`) || len(v.Daily) != 30 || v.Daily[29].Day != today ||
			v.Daily[29].In == nil || v.Daily[0].In != nil {
			t.Fatalf("range %q: previous and daily: %s", rng, raw)
		}
		for _, key := range []string{"todayRx", "todayTx", "peakRx", "peakTx"} {
			if !strings.Contains(string(raw), `"`+key+`":`) {
				t.Errorf("range %q lacks %s: %s", rng, key, raw)
			}
		}
	}
}

// feed is a sampler whose snapshots the test publishes.
type feed struct {
	mu   sync.Mutex
	subs []chan system.Snapshot
}

func (f *feed) Snapshot() system.Snapshot  { return system.Snapshot{Notices: []string{"initial"}} }
func (f *feed) History() []system.HostView { return nil }
func (f *feed) HostHistory(context.Context, string) (system.HostSeries, error) {
	return system.HostSeries{}, nil
}
func (f *feed) Network(context.Context, string) (system.TrafficView, error) {
	return system.TrafficView{}, nil
}
func (f *feed) Spark() system.Spark { return system.Spark{} }
func (f *feed) Subscribe() (<-chan system.Snapshot, func()) {
	ch := make(chan system.Snapshot, 16)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()
	return ch, func() {}
}

func (f *feed) publish(snap system.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs {
		ch <- snap
	}
}

func TestSystemEventsThrottled(t *testing.T) {
	f := &feed{}
	e := newServicesEnv(t, func(s *Server) { s.System, s.systemEvery = f, 300*time.Millisecond })
	s := e.openEvents(timeout(t))
	s.until("snapshot", nil)
	opened := time.Now()
	if data := s.until("system", nil); !strings.Contains(data, `"initial"`) {
		t.Fatalf("first system event = %s, want the current snapshot", data)
	}
	base := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		f.publish(system.Snapshot{At: base.Add(time.Duration(i) * time.Second)})
	}
	var got system.Snapshot
	if json.Unmarshal([]byte(s.until("system", nil)), &got) != nil || !got.At.Equal(base.Add(4*time.Second)) {
		t.Fatalf("throttled event is of %v, want the latest", got.At)
	}
	if since := time.Since(opened); since < 250*time.Millisecond {
		t.Fatalf("second system event after %v, want at least the throttle interval", since)
	}
	for end := time.Now().Add(400 * time.Millisecond); time.Now().Before(end); {
		if name, _, ok := s.next(); !ok || name == "system" {
			t.Fatalf("after the latest sample: %q (stream open: %v), want only pings", name, ok)
		}
	}
}

func TestSystemEventsInOrder(t *testing.T) {
	f := &feed{}
	const every = 10 * time.Millisecond
	e := newServicesEnv(t, func(s *Server) { s.System, s.systemEvery = f, every })
	s := e.openEvents(timeout(t))
	s.until("system", nil)
	base := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	const n = 300
	start := time.Now()
	go func() {
		for i := 1; i <= n; i++ {
			f.publish(system.Snapshot{At: base.Add(time.Duration(i) * time.Second)})
			time.Sleep(time.Millisecond / 2)
		}
	}()
	var last time.Time
	events := 0
	for !last.Equal(base.Add(n * time.Second)) {
		var got system.Snapshot
		if json.Unmarshal([]byte(s.until("system", nil)), &got) != nil {
			t.Fatal("system event is not a snapshot")
		}
		if !got.At.After(last) {
			t.Fatalf("system event of %v after one of %v", got.At, last)
		}
		last = got.At
		events++
	}
	if most := int(time.Since(start)/every) + 2; events > most {
		t.Fatalf("%d system events in %v, want at most one per %v (%d)", events, time.Since(start), every, most)
	}
}

func TestLocalListReportsUnreadableDocker(t *testing.T) {
	f := &fakeCollector{}
	option := withDocker(f)
	f.docker = false // configured, but the collector cannot read it
	e := newServicesEnv(t, option)
	eventually(t, "the first sample", func() bool { return len(e.srv.System.Snapshot().Notices) > 0 })
	_, found, errs := e.localList()
	if len(found) != 0 || len(errs) != 1 || errs[0].Target != "docker" || errs[0].Message != system.NoticeDocker {
		t.Fatalf("local = %+v %+v", found, errs)
	}
}
