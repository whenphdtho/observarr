package docker

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// listDocker serves a list of n running containers (IDs from id) whose inspects wait for release, if set.
type listDocker struct {
	mu       sync.Mutex
	list     string
	inspects map[string]int
	inFlight atomic.Int32
	peak     atomic.Int32
	release  chan struct{}
	events   chan string // nil: /events is refused, like a socket proxy with EVENTS=0
	health   string
}

func id(i int) string { return fmt.Sprintf("%064x", i) }

func running(n int, extra ...string) string {
	items := extra
	for i := range n {
		items = append(items, fmt.Sprintf(`{"Id":%q,"Names":["/c%d"],"State":"running"}`, id(i), i))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func (d *listDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch p := r.URL.Path; {
	case p == "/version":
		fmt.Fprint(w, `{"ApiVersion":"1.47"}`)
	case strings.HasSuffix(p, "/containers/json"):
		d.mu.Lock()
		fmt.Fprint(w, d.list)
		d.mu.Unlock()
	case strings.HasSuffix(p, "/events"):
		if d.events == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.(http.Flusher).Flush()
		for {
			select {
			case line := <-d.events:
				fmt.Fprintln(w, line)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	default:
		cid := strings.TrimSuffix(strings.TrimPrefix(p, "/v1.47/containers/"), "/json")
		d.mu.Lock()
		d.inspects[cid]++
		health := cmp.Or(d.health, "healthy")
		d.mu.Unlock()
		n := d.inFlight.Add(1)
		defer d.inFlight.Add(-1)
		for p := d.peak.Load(); n > p && !d.peak.CompareAndSwap(p, n); p = d.peak.Load() {
		}
		if d.release != nil {
			select {
			case <-d.release:
			case <-r.Context().Done():
			}
		}
		fmt.Fprintf(w, `{"RestartCount":1,"State":{"Pid":7,"Health":{"Status":%q}}}`, health)
	}
}

func (d *listDocker) count(cid string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.inspects[cid]
}

func newListDocker(t *testing.T, d *listDocker) *Client {
	t.Helper()
	d.inspects = map[string]int{}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	c, err := New("tcp://" + srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInspectOnlyNewOrChanged(t *testing.T) {
	d := &listDocker{list: running(2), events: make(chan string, 1)}
	c := newListDocker(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		cs, err := c.Containers(ctx)
		if err != nil || len(cs) != 2 || cs[0].PID != 7 || cs[1].RestartCount != 1 {
			t.Fatalf("containers = %+v, %v", cs, err)
		}
	}
	if d.count(id(0)) != 1 || d.count(id(1)) != 1 {
		t.Fatalf("inspects = %v, want each container once", d.inspects)
	}

	d.events <- fmt.Sprintf(`{"Type":"container","Action":"restart","Actor":{"ID":%q}}`, id(0))
	<-ch
	if _, err := c.Containers(ctx); err != nil {
		t.Fatal(err)
	}
	if d.count(id(0)) != 2 || d.count(id(1)) != 1 {
		t.Fatalf("inspects = %v, want only the container with an event inspected again", d.inspects)
	}

	d.mu.Lock()
	d.list = strings.Replace(running(2), `"running"`, `"paused"`, 1)
	d.mu.Unlock()
	if _, err := c.Containers(ctx); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.list = running(2)
	d.mu.Unlock()
	if _, err := c.Containers(ctx); err != nil {
		t.Fatal(err)
	}
	if d.count(id(0)) != 3 || d.count(id(1)) != 1 {
		t.Fatalf("inspects = %v, want a container inspected again after its state changed", d.inspects)
	}
}

func TestContainersCappedAndIDsChecked(t *testing.T) {
	d := &listDocker{list: running(MaxContainers+1,
		`{"Id":"abc","Names":["/short"],"State":"running"}`,
		`{"Id":"../../x`+strings.Repeat("0", 58)+`","Names":["/bad"],"State":"running"}`)}
	c := newListDocker(t, d)
	cs, err := c.Containers(context.Background())
	if !errors.Is(err, ErrTooMany) || errors.Is(err, ErrInspect) {
		t.Fatalf("err = %v, want ErrTooMany only", err)
	}
	if len(cs) != MaxContainers || cs[0].ID != id(0) {
		t.Fatalf("%d containers, first %q; want the first %d with valid IDs", len(cs), cs[0].ID, MaxContainers)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.inspects["abc"] != 0 || len(d.inspects) != MaxContainers {
		t.Fatalf("%d containers inspected, want exactly the %d listed", len(d.inspects), MaxContainers)
	}
}

func TestInspectsWaitForASlotBeforeStarting(t *testing.T) {
	d := &listDocker{list: running(500), release: make(chan struct{})}
	c := newListDocker(t, d)
	before := runtime.NumGoroutine()
	done := make(chan error, 1)
	go func() { _, err := c.Containers(context.Background()); done <- err }()

	deadline := time.Now().Add(5 * time.Second)
	for d.inFlight.Load() < inspects {
		if time.Now().After(deadline) {
			t.Fatal("inspects did not start")
		}
		time.Sleep(time.Millisecond)
	}
	// Each inspect in flight takes a few goroutines (client and server side); 500 waiting ones would show.
	if n := runtime.NumGoroutine() - before; n > 100 {
		t.Fatalf("%d more goroutines while %d inspects run", n, inspects)
	}
	close(d.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p := d.peak.Load(); p > inspects {
		t.Fatalf("%d inspects at once, want at most %d", p, inspects)
	}
}

func TestNoCacheWithoutEvents(t *testing.T) {
	d := &listDocker{list: running(1)}
	c := newListDocker(t, d)
	ctx := context.Background()
	if _, err := c.Events(ctx); !errors.Is(err, ErrDenied) {
		t.Fatalf("events err = %v, want ErrDenied", err)
	}
	health := func() string {
		t.Helper()
		cs, err := c.Containers(ctx)
		if err != nil || len(cs) != 1 {
			t.Fatalf("containers = %+v, %v", cs, err)
		}
		return cs[0].Health
	}
	if h := health(); h != "healthy" {
		t.Fatalf("health = %q", h)
	}
	d.mu.Lock()
	d.health = "unhealthy"
	d.mu.Unlock()
	if h := health(); h != "unhealthy" {
		t.Fatalf("health = %q, want the change seen without an events stream", h)
	}
}

func TestCacheDroppedWhenTheStreamEnds(t *testing.T) {
	d := &listDocker{list: running(1), events: make(chan string)}
	c := newListDocker(t, d)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Containers(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel()
	for range ch {
	}
	for range 2 {
		if _, err := c.Containers(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := d.count(id(0)); n != 3 {
		t.Fatalf("%d inspects, want one per list once the stream ended", n)
	}
}
