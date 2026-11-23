package api

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"observarr/internal/collect"
	"observarr/internal/state"
	"observarr/internal/system"
)

// fakeCollector serves a container list and events once docker is set; the host is never available.
type fakeCollector struct {
	mu     sync.Mutex
	docker bool
	cs     []collect.Container
	usage  []collect.ContainerUsage
	events chan collect.Event
}

func (f *fakeCollector) Host(context.Context) (collect.Host, error) {
	return collect.Host{}, collect.ErrUnavailable
}

func (f *fakeCollector) Containers(context.Context) ([]collect.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.docker {
		return nil, collect.ErrUnavailable
	}
	return slices.Clone(f.cs), nil
}

func (f *fakeCollector) Usage(context.Context, []collect.Container) ([]collect.ContainerUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.usage), nil
}

func (f *fakeCollector) Events(ctx context.Context) (<-chan collect.Event, error) {
	if !f.docker {
		return nil, collect.ErrUnavailable
	}
	out := make(chan collect.Event)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-f.events:
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// set changes a container and tells the sampler, as Docker would with an event.
func (f *fakeCollector) set(name string, change func(*collect.Container)) {
	f.mu.Lock()
	for i := range f.cs {
		if f.cs[i].Name == name {
			change(&f.cs[i])
		}
	}
	f.mu.Unlock()
	f.events <- collect.Event{Name: name, Action: "die"}
}

// withDocker makes the server's sampler read f, which lists one running, healthy container "app".
func withDocker(f *fakeCollector) func(*Server) {
	f.docker, f.events = true, make(chan collect.Event)
	f.cs = []collect.Container{{ID: "a1", Name: "app", Image: "fake:1", State: "running", Health: "healthy",
		RestartCount: 2, PID: 42}}
	f.usage = []collect.ContainerUsage{{ID: "a1", MemBytes: 123 << 20}}
	return func(s *Server) {
		s.Config.DockerHost = "tcp://proxy:2375"
		s.System = system.New(f, s.Hub.Watchers, s.Log)
	}
}

// serviceState returns the service's state from GET /api/services.
func (e *servicesEnv) serviceState(id int64) state.ServiceState {
	e.t.Helper()
	code, raw, _ := e.raw("GET", "/api/services", "", nil)
	var all []state.ServiceState
	if code != 200 || json.Unmarshal(raw, &all) != nil {
		e.t.Fatalf("GET /api/services = %d %s", code, raw)
	}
	for _, st := range all {
		if st.ID == id {
			return st
		}
	}
	e.t.Fatalf("service %d not listed", id)
	return state.ServiceState{}
}

func TestContainerLink(t *testing.T) {
	f := &fakeCollector{}
	e := newServicesEnv(t, withDocker(f))
	eventually(t, "the first sample", func() bool { return len(e.srv.System.Snapshot().Containers) == 1 })
	code, data := e.send("POST", "/api/services", `{"type":"keyless","name":"Linked","url":"`+e.appURL+
		`/open","container":"app"}`)
	if code != 201 {
		t.Fatalf("create = %d %v", code, data)
	}
	id := int64(data["id"].(float64))
	want := state.ContainerBrief{Name: "app", State: "running", Health: "healthy", MemBytes: 123 << 20, Restarts: 2}
	if c := e.serviceState(id).Container; c == nil || *c != want {
		t.Fatalf("brief = %+v, want %+v", c, want)
	}

	for _, bad := range []string{`"-app"`, `"a/b"`, `"a b"`, `"` + strings.Repeat("a", 256) + `"`, `1`} {
		if code, _ := e.send("PATCH", e.path(id), `{"container":`+bad+`}`); code != 400 {
			t.Errorf("container %s = %d, want 400", bad, code)
		}
	}
	if code, _ := e.send("PATCH", e.path(id), `{"container":"gone"}`); code != 200 {
		t.Fatalf("relink = %d", code)
	}
	if c := e.serviceState(id).Container; c == nil || *c != (state.ContainerBrief{Name: "gone"}) {
		t.Fatalf("brief of an unknown container = %+v", c)
	}
	if code, _ := e.send("PATCH", e.path(id), `{"container":""}`); code != 200 {
		t.Fatalf("unlink = %d", code)
	}
	if c := e.serviceState(id).Container; c != nil {
		t.Fatalf("brief after unlinking = %+v", c)
	}
	if svc, _ := e.srv.Store.ServiceByID(context.Background(), id); svc.Container != "" {
		t.Fatalf("stored link after unlinking = %q", svc.Container)
	}
}

func TestContainerBriefFollowsTheSampler(t *testing.T) {
	f := &fakeCollector{}
	e := newServicesEnv(t, withDocker(f))
	code, data := e.send("POST", "/api/services", `{"type":"keyless","name":"Linked","url":"`+e.appURL+
		`/open","container":"app"}`)
	if code != 201 {
		t.Fatalf("create = %d %v", code, data)
	}
	id := int64(data["id"].(float64))
	s := e.openEvents(timeout(t))
	f.set("app", func(c *collect.Container) { c.State, c.Health, c.PID = "exited", "", 0 })
	s.until("service", func(data string) bool {
		var st state.ServiceState
		return json.Unmarshal([]byte(data), &st) == nil && st.ID == id && st.Container != nil &&
			*st.Container == state.ContainerBrief{Name: "app", State: "exited", Restarts: 2}
	})
}

func TestBriefIgnoresSmallChanges(t *testing.T) {
	view := func(cpu float64, mem uint64) []system.ContainerView {
		return []system.ContainerView{{Container: collect.Container{Name: "app", State: "running"}, CPUPercent: cpu,
			MemBytes: mem}}
	}
	a, b := brief("app", view(1.04, 100<<20+1234)), brief("app", view(0.96, 100<<20-4321))
	if *a != *b || a.MemBytes != 100<<20 || a.CPUPercent != 1 {
		t.Fatalf("briefs %+v and %+v, want both at 1 %% and 100 MiB", a, b)
	}
	if c := brief("app", view(1, 101<<20)); c.MemBytes != 101<<20 {
		t.Fatalf("a change of a MiB is lost: %+v", c)
	}
}
