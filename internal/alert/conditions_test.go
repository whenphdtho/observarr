package alert

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"observarr/internal/collect"
	"observarr/internal/shape"
	"observarr/internal/state"
	"observarr/internal/system"
)

func svc(id int64, name string, up bool, readings map[shape.Kind]shape.Reading, kinds ...shape.Kind) state.ServiceState {
	if up && readings[shape.KindHealth].Data == nil {
		readings[shape.KindHealth] = shape.Reading{Data: shape.Health{Warnings: []shape.Warning{}}}
	}
	return state.ServiceState{ID: id, Name: name, Up: up, Readings: readings, Kinds: kinds}
}

func keysOf(conds []Condition) []string {
	var out []string
	for _, c := range conds {
		out = append(out, c.Key+" "+string(c.Severity))
	}
	return out
}

func TestEvaluateServices(t *testing.T) {
	health := shape.Health{Warnings: []shape.Warning{
		{Level: shape.LevelInfo, Message: "an update is available"},
		{Level: shape.LevelWarning, Message: "indexer unavailable", Source: "IndexerCheck"},
		{Level: shape.LevelError, Message: "cache out of space", Source: "media:"},
	}}
	queue := shape.Queue{Items: []shape.QueueItem{
		{Title: "Sintel", Status: "downloading"},
		{Title: "Big Buck Bunny", Status: "warning", Problem: "no files found are eligible for import"},
		{Title: "Tears of Steel", Status: "failed"},
	}}
	mounts := shape.Mounts{Mounts: []shape.Mount{
		{Remote: "media:", State: shape.MountMounted, OutOfSpace: true}, // reported as a warning already
		{Remote: "backup:", State: shape.MountUnmounted},
		{Remote: "cache:", State: shape.MountMounted, Cache: true, CacheUsedBytes: 95, CacheMaxBytes: 100},
		{Remote: "fine:", State: shape.MountMounted, Cache: true, CacheUsedBytes: 10, CacheMaxBytes: 100},
	}}
	in := Input{Now: t0, Services: []state.ServiceState{
		svc(1, "Down", false, map[shape.Kind]shape.Reading{shape.KindHealth: {Error: "connection refused"}}),
		svc(2, "Busy", true, map[shape.Kind]shape.Reading{shape.KindHealth: {Data: health},
			shape.KindQueue: {Data: queue}, shape.KindMounts: {Data: mounts}}, shape.KindQueue, shape.KindMounts),
		svc(3, "Asks", true, map[shape.Kind]shape.Reading{shape.KindRequests: {Data: shape.Requests{Counts: shape.RequestCounts{Pending: 2}}}}),
		svc(4, "New", false, map[shape.Kind]shape.Reading{}), // not checked yet
	}}
	r := DefaultRules()
	r.Enabled[RuleRequests] = true
	conds, known := Evaluate(in, r)
	got := keysOf(conds)
	for _, want := range []string{"down/1 critical", "download/2/", "mount/2/", "cache/2/", "requests/3 info"} {
		if !slices.ContainsFunc(got, func(k string) bool { return strings.HasPrefix(k, want) }) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if n := len(got); n != 8 { // down, 2 warnings, 2 downloads, unmounted, cache, requests; nothing for 4
		t.Errorf("conditions = %q (%d)", got, n)
	}
	for _, c := range conds {
		if c.Service == 2 && c.Rule == RuleHealth && c.View != "#/storage" {
			t.Errorf("a mount app's warning leads to %s", c.View)
		}
		// The figure that sums a condition up, where it has one.
		want := map[string]string{"cache/": "95%", "requests/": "2", "down/": ""}
		for prefix, v := range want {
			if strings.HasPrefix(c.Key, prefix) && c.Value != v {
				t.Errorf("%s: value %q, want %q", c.Key, c.Value, v)
			}
		}
		if c.Key == "down/1" && c.Title != "Down is not reachable" {
			t.Errorf("down title = %q", c.Title)
		}
	}
	for _, s := range []string{"1/down", "2/health", "2/queue", "2/mounts", "3/requests"} {
		if !known[s] {
			t.Errorf("scope %s not known", s)
		}
	}
	if known["1/health"] || known["4/down"] {
		t.Errorf("known = %v: a service down or not checked tells nothing of its warnings", known)
	}

	r.Enabled[RuleDownloads], r.Enabled[RuleRequests] = false, false
	conds, _ = Evaluate(in, r)
	if n := len(conds); n != 5 {
		t.Errorf("with downloads and requests off: %q", keysOf(conds))
	}
}

func TestEvaluateHostAndSecurity(t *testing.T) {
	snap := system.Snapshot{At: t0, Host: &system.HostView{Disks: []collect.Disk{
		{Path: "/", Total: 100, Free: 5, Avail: 5},
		{Path: "/data", Total: 100, Free: 5, Avail: 5}, // the same file system
		{Path: "/media", Total: 100, Free: 50, Avail: 50},
		{Path: "/full", Total: 1000, Free: 10, Avail: 0},
	}}, Containers: []system.ContainerView{
		{Container: collect.Container{Name: "app", State: "running", Health: "unhealthy"}},
		{Container: collect.Container{Name: "loop", State: "restarting"}},
		{Container: collect.Container{Name: "ok", State: "running"}},
	}, Traffic: &system.TrafficSummary{Budget: &system.Budget{Period: "month", Limit: 100, Used: 85}}}
	in := Input{Now: t0, System: snap, Security: SecurityState{LockedOut: 1, Failed: 12, ServerLocked: time.Minute}}
	conds, known := Evaluate(in, DefaultRules())
	want := []string{"container/app critical", "container/loop warning", "disk// warning", "disk//full critical",
		"traffic/80/2026-11 warning", "security/lockout warning", "security/limited warning", "security/failed warning"}
	if got := keysOf(conds); !slices.Equal(got, want) {
		t.Fatalf("conditions = %q\nwant %q", got, want)
	}
	if !known["host"] || !known["containers"] || !known["traffic"] || !known["security"] {
		t.Fatalf("known = %v", known)
	}
	var values []string
	for _, c := range conds {
		values = append(values, c.Value)
	}
	if want := []string{"", "", "95%", "100%", "85%", "1", "", "12"}; !slices.Equal(values, want) {
		t.Fatalf("values = %q\nwant %q", values, want)
	}

	in.System.Traffic.Budget.Used = 100
	conds, _ = Evaluate(in, DefaultRules())
	if !slices.Contains(keysOf(conds), "traffic/100/2026-11 critical") {
		t.Fatalf("at 100 %%: %q", keysOf(conds))
	}

	// Before the first sample, and without Docker, nothing is known of the host or the containers.
	in.System = system.Snapshot{}
	if _, known := Evaluate(in, DefaultRules()); known["host"] || known["containers"] {
		t.Fatalf("known before the first sample = %v", known)
	}
	in.System = system.Snapshot{At: t0, Notices: []string{system.NoticeDockerError}}
	if _, known := Evaluate(in, DefaultRules()); known["containers"] {
		t.Fatal("containers known while Docker cannot be read")
	}
}

// A health warning keeps its key while its text changes (a count that goes up is the same problem), and two
// warnings alike in every way stay two conditions.
func TestConditionKeys(t *testing.T) {
	read := func(n int) []Condition {
		health := shape.Health{Warnings: []shape.Warning{
			{Level: shape.LevelWarning, Message: strconv.Itoa(n) + " requests waiting for approval"},
			{Level: shape.LevelWarning, Message: "a request failed"},
			{Level: shape.LevelError, Message: "a request failed"},
		}}
		queue := shape.Queue{Items: []shape.QueueItem{
			{Title: "Sintel", Status: "failed"}, {Title: "Sintel", Status: "failed"}, {Title: "Sintel#1", Status: "failed"},
		}}
		conds, _ := Evaluate(Input{Now: t0, Services: []state.ServiceState{
			svc(7, "Asks", true, map[shape.Kind]shape.Reading{shape.KindHealth: {Data: health},
				shape.KindQueue: {Data: queue}}, shape.KindQueue),
		}}, DefaultRules())
		return conds
	}
	first, second := read(3), read(4)
	if len(first) != 6 || len(second) != 6 {
		t.Fatalf("conditions = %q", keysOf(first))
	}
	keys := map[string]bool{}
	for i := range first {
		if first[i].Key != second[i].Key {
			t.Errorf("key changed with the text: %s -> %s", first[i].Key, second[i].Key)
		}
		if keys[first[i].Key] {
			t.Errorf("key %s given twice", first[i].Key)
		}
		keys[first[i].Key] = true
	}
	if second[0].Message != "4 requests waiting for approval" {
		t.Errorf("message = %q", second[0].Message)
	}
}
