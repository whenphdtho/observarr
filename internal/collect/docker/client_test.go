package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"observarr/internal/collect"
)

const listJSON = `[
 {"Id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Names":["/sonarr"],"Image":"lscr.io/linuxserver/sonarr:latest","State":"running","Status":"Up 2 hours (healthy)",
  "Ports":[{"IP":"::","PrivatePort":8989,"PublicPort":8989,"Type":"tcp"},{"IP":"0.0.0.0","PrivatePort":8989,"PublicPort":8989,"Type":"tcp"},{"PrivatePort":1900,"Type":"udp"}],
  "Labels":{"com.docker.compose.project":"media","observarr.type":"sonarr","org.opencontainers.image.version":"4.0"},
  "HostConfig":{"NetworkMode":"media_default"}},
 {"Id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","Names":["/web/db","/db"],"Image":"postgres:16","State":"exited","Status":"Exited (0) 3 days ago","Ports":[],"Labels":{}}
]`

const inspectJSON = `{"Id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","RestartCount":3,"State":{"Status":"running","Pid":4242,"StartedAt":"2026-11-22T08:00:00.123456789Z","Health":{"Status":"healthy","FailingStreak":0}}}`

// fakeDocker serves a minimal Engine API and records every request's method.
type fakeDocker struct {
	mu      sync.Mutex
	methods []string
	paths   []string
	version string
	deny    string // path prefix answered with 403, like a socket proxy with the endpoint disabled
	events  chan string
}

func (f *fakeDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.methods = append(f.methods, r.Method)
	f.paths = append(f.paths, r.URL.RequestURI())
	f.mu.Unlock()
	p := r.URL.Path
	if f.deny != "" && strings.Contains(p, f.deny) {
		http.Error(w, "<html><body><h1>403 Forbidden</h1></body></html>", http.StatusForbidden)
		return
	}
	switch {
	case p == "/version":
		fmt.Fprintf(w, `{"Version":"29.0.1","ApiVersion":%q,"MinAPIVersion":"1.44"}`, f.version)
	case strings.HasSuffix(p, "/containers/json"):
		fmt.Fprint(w, listJSON)
	case strings.HasSuffix(p, "/containers/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/json"):
		fmt.Fprint(w, inspectJSON)
	case strings.HasSuffix(p, "/events"):
		w.Header().Set("Content-Type", "application/json")
		w.(http.Flusher).Flush()
		for {
			select {
			case line, ok := <-f.events:
				if !ok {
					return
				}
				fmt.Fprintln(w, line)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeDocker) assertGETOnly(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.methods) == 0 {
		t.Fatal("no requests recorded")
	}
	for i, m := range f.methods {
		if m != http.MethodGet {
			t.Errorf("request %s used %s: the client must only GET", f.paths[i], m)
		}
	}
}

func newFake(t *testing.T, f *fakeDocker) *Client {
	t.Helper()
	if f.version == "" {
		f.version = "1.47"
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { f.assertGETOnly(t) })
	c, err := New("tcp://" + srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestContainers(t *testing.T) {
	f := &fakeDocker{}
	c := newFake(t, f)
	got, err := c.Containers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []collect.Container{
		{
			ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "sonarr", Image: "lscr.io/linuxserver/sonarr:latest", State: "running", Health: "healthy",
			StartedAt:    time.Date(2026, 11, 22, 8, 0, 0, 123456789, time.UTC),
			RestartCount: 3, PID: 4242, NetworkMode: "media_default",
			Ports:  []collect.Port{{Private: 1900, Proto: "udp"}, {Private: 8989, Public: 8989, Proto: "tcp"}},
			Labels: map[string]string{"com.docker.compose.project": "media", "observarr.type": "sonarr"},
		},
		{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "db", Image: "postgres:16", State: "exited"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	wantPaths := []string{"/version", "/v1.47/containers/json?all=1", "/v1.47/containers/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/json"}
	if !reflect.DeepEqual(f.paths, wantPaths) {
		t.Fatalf("paths = %v, want %v (only running containers are inspected)", f.paths, wantPaths)
	}
}

func TestVersionNegotiatedOnceAndCapped(t *testing.T) {
	f := &fakeDocker{version: "9.99"}
	c := newFake(t, f)
	for range 2 {
		if _, err := c.Containers(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if n := strings.Count(strings.Join(f.paths, " "), "/version"); n != 1 {
		t.Fatalf("/version requested %d times, want once: %v", n, f.paths)
	}
	if !strings.HasPrefix(f.paths[1], "/v"+maxAPI+"/") {
		t.Fatalf("a newer daemon must be spoken to at our newest known version: %v", f.paths)
	}
}

func TestBadVersionRefused(t *testing.T) {
	for _, v := range []string{"1.47/../../x", "abc", "1.", "1.47 "} {
		c := newFake(t, &fakeDocker{version: v})
		if _, err := c.Containers(context.Background()); err == nil || !strings.Contains(err.Error(), "API version") {
			t.Errorf("ApiVersion %q: err = %v, want an API version error", v, err)
		}
	}
}

func TestProxyDenied(t *testing.T) {
	c := newFake(t, &fakeDocker{deny: "/containers"})
	_, err := c.Containers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "the socket proxy does not allow /containers/json") {
		t.Fatalf("err = %v", err)
	}
	c = newFake(t, &fakeDocker{deny: "/events"})
	if _, err := c.Events(context.Background()); err == nil || !strings.Contains(err.Error(), "does not allow /events") {
		t.Fatalf("events err = %v", err)
	}
}

func TestInspectDenied(t *testing.T) {
	c := newFake(t, &fakeDocker{deny: "/containers/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/"})
	cs, err := c.Containers(context.Background())
	if !errors.Is(err, ErrInspect) || !strings.Contains(err.Error(), "the socket proxy does not allow /containers/{id}/json") {
		t.Fatalf("err = %v, want ErrInspect naming the endpoint", err)
	}
	if len(cs) != 2 || cs[0].Name != "sonarr" || cs[0].PID != 0 {
		t.Fatalf("the list must still be returned: %+v", cs)
	}
}

func TestResponseCap(t *testing.T) {
	big := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			fmt.Fprint(w, `{"ApiVersion":"1.47"}`)
			return
		}
		fmt.Fprint(w, `[{"Id":"`+strings.Repeat("a", maxBody)+`"}]`)
	})
	srv := httptest.NewServer(big)
	defer srv.Close()
	c, _ := New("tcp://" + srv.Listener.Addr().String())
	if _, err := c.Containers(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want a size error", err)
	}
}

func TestEvents(t *testing.T) {
	f := &fakeDocker{events: make(chan string, 8)}
	c := newFake(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.events <- `{"status":"start","id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Type":"container","Action":"start","Actor":{"ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Attributes":{"image":"x","name":"sonarr"}},"scope":"local","time":1763800000,"timeNano":1763800000123456789}`
	f.events <- `not json`
	f.events <- `{"Type":"network","Action":"connect","Actor":{"ID":"n1"}}`
	f.events <- `{"Type":"container","Action":"exec_start: sh -c curl -f localhost","Actor":{"ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Attributes":{"name":"sonarr"}},"time":1763800004}`
	f.events <- `{"Type":"container","Action":"health_status: unhealthy","Actor":{"ID":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","Attributes":{"name":"sonarr"}},"time":1763800005}`
	wantEv := []collect.Event{ // the exec_ event is skipped even if the daemon sends it
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "sonarr", Action: "start", At: time.Unix(0, 1763800000123456789)},
		{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "sonarr", Action: "health_status", At: time.Unix(1763800005, 0)},
	}
	for _, w := range wantEv {
		select {
		case got := <-ch:
			if !reflect.DeepEqual(got, w) {
				t.Fatalf("got %+v, want %+v", got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
	}
	f.mu.Lock()
	last := f.paths[len(f.paths)-1]
	f.mu.Unlock()
	u, _ := url.Parse(last)
	want := `{"type":["container"],"event":["create","start","restart","die","stop","kill","oom","pause","unpause","destroy","rename","health_status"]}`
	if u.Path != "/v1.47/events" || u.Query().Get("filters") != want {
		t.Fatalf("events request = %s", last)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected event after cancel")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed after ctx ended")
	}
}

func TestEventsStreamEndCloses(t *testing.T) {
	f := &fakeDocker{events: make(chan string)}
	c := newFake(t, f)
	ch, err := c.Events(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	close(f.events)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("channel not closed when the stream ended")
	}
}

func TestUnixSocket(t *testing.T) {
	dir, err := os.MkdirTemp("", "dk") // short: socket paths are limited to ~104 bytes on macOS
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeDocker{version: "1.47"}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: f}}
	srv.Start()
	defer srv.Close()
	c, err := New("unix://" + sock)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := c.Containers(context.Background())
	if err != nil || len(cs) != 2 {
		t.Fatalf("containers = %v, %v", cs, err)
	}
	f.assertGETOnly(t)
}

func TestNewRejects(t *testing.T) {
	for _, h := range []string{"", "proxy:2375", "http://proxy:2375", "tcp://proxy", "tcp://proxy:2375/x", "tcp://u:p@proxy:2375",
		"tcp://proxy:2375?a=b", "unix://", "unix://host/x.sock", "unix:relative.sock"} {
		if _, err := New(h); err == nil {
			t.Errorf("New(%q): want an error", h)
		}
	}
	for _, h := range []string{"tcp://socket-proxy:2375", "tcp://10.0.0.2:2375", "tcp://[::1]:2375", "unix:///var/run/docker.sock"} {
		if _, err := New(h); err != nil {
			t.Errorf("New(%q): %v", h, err)
		}
	}
}

func TestNoRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.invalid/version", http.StatusFound)
	}))
	defer srv.Close()
	c, _ := New("tcp://" + srv.Listener.Addr().String())
	if _, err := c.Containers(context.Background()); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("err = %v, want the redirect reported as a status", err)
	}
}

func TestHugeResponseHeadersRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Big", strings.Repeat("a", 100<<10))
		fmt.Fprint(w, `{"ApiVersion":"1.47"}`)
	}))
	defer srv.Close()
	c, _ := New("tcp://" + srv.Listener.Addr().String())
	if _, err := c.Containers(context.Background()); err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("err = %v, want a header size error", err)
	}
}
