package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"observarr/internal/shape"
	"observarr/internal/state"
)

// stream is an open /api/events response.
type stream struct {
	t    *testing.T
	resp *http.Response
	sc   *bufio.Scanner
	seen *syncBuffer
}

// openEvents opens the live event stream; it is closed when the test ends.
func (c *client) openEvents(ctx context.Context) *stream {
	c.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+"/api/events", nil)
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	c.t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 {
		c.t.Fatalf("events = %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	return &stream{t: c.t, resp: resp, sc: sc, seen: c.seen}
}

// next returns the next event's name and data. ok is false once the stream ended.
func (s *stream) next() (name, data string, ok bool) {
	for s.sc.Scan() {
		line := s.sc.Text()
		s.seen.Write([]byte(line + "\n"))
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && name != "":
			return name, data, true
		}
	}
	return "", "", false
}

// until reads events until one called name for which match (if given) is true.
func (s *stream) until(name string, match func(data string) bool) string {
	s.t.Helper()
	for {
		n, data, ok := s.next()
		if !ok {
			s.t.Fatalf("stream ended (%v) before a %q event", s.sc.Err(), name)
		}
		if n == name && (match == nil || match(data)) {
			return data
		}
	}
}

func timeout(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestEventsSnapshotThenChanges(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	s := e.openEvents(timeout(t))
	if h := s.resp.Header; h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-store" ||
		h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers %v", h)
	}

	// The browser is told to wait a random 2 to 4 s before reconnecting.
	if !s.sc.Scan() {
		t.Fatal("stream ended at once")
	}
	if ms, err := strconv.Atoi(strings.TrimPrefix(s.sc.Text(), "retry: ")); err != nil || ms < 2000 || ms >= 4000 {
		t.Fatalf("first line %q, want the reconnection delay", s.sc.Text())
	}
	name, data, ok := s.next()
	var snap []state.ServiceState
	if !ok || name != "snapshot" || json.Unmarshal([]byte(data), &snap) != nil || len(snap) != 1 || snap[0].ID != id {
		t.Fatalf("first event = %q %s, want the snapshot", name, data)
	}

	e.send("PATCH", e.path(id), `{"name":"Renamed"}`)
	s.until("service", func(data string) bool {
		var st state.ServiceState
		return json.Unmarshal([]byte(data), &st) == nil && st.ID == id && st.Name == "Renamed"
	})
	e.srv.Hub.Record(id, shape.KindHealth, shape.Reading{Error: "down for a test", At: time.Now()})
	s.until("service", func(data string) bool { return strings.Contains(data, "down for a test") })
	s.until("ping", nil)
	e.send("DELETE", e.path(id), "")
	if data := s.until("removed", nil); data != `{"id":`+strconv.FormatInt(id, 10)+`}` {
		t.Fatalf("removed data = %s", data)
	}
}

func TestEventsEndWhenClientLeaves(t *testing.T) {
	e := newServicesEnv(t)
	ctx, cancel := context.WithCancel(timeout(t))
	s := e.openEvents(ctx)
	s.until("snapshot", nil)
	if e.srv.Hub.Watchers() != 1 {
		t.Fatal("the stream does not watch the hub")
	}
	cancel()
	eventually(t, "the handler ends", func() bool { return e.srv.Hub.Watchers() == 0 && e.srv.streams.Load() == 0 })
}

func TestEventsEndWhenHubDropsSlowClient(t *testing.T) {
	e := newServicesEnv(t)
	s := e.openEvents(timeout(t))
	s.until("snapshot", nil)
	// Not reading while the hub sends far more than the connection buffers and the subscription hold: the
	// handler blocks writing, its subscription fills up and the hub closes it.
	name := strings.Repeat("n", 200)
	for i := range 20000 {
		e.srv.Hub.Put(state.ServiceState{ID: int64(i), Name: name, Kinds: []shape.Kind{shape.KindHealth}})
	}
	for {
		if _, _, ok := s.next(); !ok {
			break
		}
	}
	if err := s.sc.Err(); err != nil {
		t.Fatalf("stream failed (%v) instead of ending", err)
	}
	eventually(t, "the handler ends", func() bool { return e.srv.Hub.Watchers() == 0 && e.srv.streams.Load() == 0 })
}

func TestEventsGetOnly(t *testing.T) {
	e := newServicesEnv(t)
	if code, _, _ := e.raw("HEAD", "/api/events", "", nil); code != 404 {
		t.Fatalf("HEAD = %d, want 404", code)
	}
	if e.srv.streams.Load() != 0 || e.srv.Hub.Watchers() != 0 {
		t.Fatal("a HEAD request took a stream slot or subscribed")
	}
}

// signedIn returns a new client of c's server with a session of its own.
func signedIn(c *client) *client {
	c.t.Helper()
	n := newClientFor(c)
	if code, _, _ := n.do("POST", "/api/login", admin, nil); code != 200 {
		c.t.Fatalf("login = %d", code)
	}
	return n
}

// refusedStream checks that c may not open another live event stream now.
func refusedStream(c *client) {
	c.t.Helper()
	code, _, h := c.do("GET", "/api/events", "", nil)
	if code != 503 || h.Get("Retry-After") != "5" {
		c.t.Fatalf("stream over the cap = %d (Retry-After %q), want 503 with Retry-After 5", code, h.Get("Retry-After"))
	}
}

func TestEventsCapped(t *testing.T) {
	e := newServicesEnv(t)
	ctx := timeout(t)
	var first *stream
	for range maxStreams / maxSessionStreams {
		c := signedIn(e.client)
		for range maxSessionStreams {
			s := c.openEvents(ctx)
			if first == nil {
				first = s
			}
		}
	}
	refusedStream(signedIn(e.client))
	first.resp.Body.Close()
	eventually(t, "a slot is free", func() bool { return e.srv.streams.Load() < maxStreams })
	signedIn(e.client).openEvents(ctx).until("snapshot", nil)
}

func TestEventsCappedPerSession(t *testing.T) {
	e := newServicesEnv(t)
	ctx := timeout(t)
	var streams []*stream
	for range maxSessionStreams {
		streams = append(streams, e.openEvents(ctx))
	}
	refusedStream(e.client)
	signedIn(e.client).openEvents(ctx).until("snapshot", nil) // another session has slots of its own
	streams[0].resp.Body.Close()
	open := int32(maxSessionStreams - 1 + 1) // this session's remaining ones and the other session's
	eventually(t, "the session's slot is free", func() bool { return e.srv.streams.Load() == open })
	e.openEvents(ctx).until("snapshot", nil)
}

func TestEventsEndAtLogout(t *testing.T) {
	e := newServicesEnv(t)
	s := e.openEvents(timeout(t))
	s.until("snapshot", nil)
	if code, _ := e.send("POST", "/api/logout", ""); code != 200 {
		t.Fatalf("logout = %d", code)
	}
	ended := make(chan struct{})
	go func() {
		for {
			if _, _, ok := s.next(); !ok {
				close(ended)
				return
			}
		}
	}()
	select {
	case <-ended:
	case <-time.After(10 * e.srv.pingEvery):
		t.Fatal("the stream outlived its session")
	}
	eventually(t, "the handler ends", func() bool { return e.srv.streams.Load() == 0 })
}

// Signing out the other sessions ends their streams at once, not at their next session check (an hour away here);
// the caller's own stream stays open.
func TestEventsEndWhenSignedOutByAnother(t *testing.T) {
	e := newServicesEnv(t, func(s *Server) { s.pingEvery = time.Hour })
	other := newClientFor(e.client)
	if code, _, _ := other.do("POST", "/api/login", admin, nil); code != 200 {
		t.Fatal("login failed")
	}
	own, ended := e.openEvents(timeout(t)), other.openEvents(timeout(t))
	own.until("snapshot", nil)
	ended.until("snapshot", nil)
	if code, _ := e.send("POST", "/api/security/sign-out-others", ""); code != 200 {
		t.Fatalf("sign out others = %d", code)
	}
	for {
		if _, _, ok := ended.next(); !ok {
			break
		}
	}
	if err := ended.sc.Err(); err != nil {
		t.Fatalf("stream failed (%v) instead of ending", err)
	}
	eventually(t, "the ended stream's slot is free", func() bool { return e.srv.streams.Load() == 1 })
}
