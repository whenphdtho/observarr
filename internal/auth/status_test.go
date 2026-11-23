package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"observarr/internal/store"
)

func TestLimiterStatus(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	if addrs, global := l.Status(); len(addrs) != 0 || global != 0 {
		t.Fatalf("new limiter: %v, %v", addrs, global)
	}
	for range MaxAttempts {
		l.Attempt("198.51.100.7")
	}
	clock.t = clock.t.Add(time.Minute)
	l.Attempt("203.0.113.9")
	clock.t = clock.t.Add(4 * time.Minute)

	addrs, global := l.Status()
	if global != 0 || len(addrs) != 2 {
		t.Fatalf("status = %+v, global %v", addrs, global)
	}
	locked, other := addrs[0], addrs[1]
	if locked.Key != "198.51.100.7" || locked.Attempts != MaxAttempts || locked.Locked != LockoutWindow-5*time.Minute {
		t.Errorf("locked address = %+v", locked)
	}
	if other.Key != "203.0.113.9" || other.Attempts != 1 || other.Locked != 0 || !other.Last.Equal(clock.t.Add(-4*time.Minute)) {
		t.Errorf("other address = %+v", other)
	}

	clock.t = clock.t.Add(LockoutWindow)
	if addrs, _ := l.Status(); len(addrs) != 0 {
		t.Fatalf("after the window: %+v", addrs)
	}
}

func TestLimiterStatusServerWide(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	l := NewLimiter(clock.now)
	for i := range GlobalAttempts {
		l.Attempt("192.0.2." + strconv.Itoa(i))
	}
	clock.t = clock.t.Add(10 * time.Second)
	if _, global := l.Status(); global != GlobalWindow-10*time.Second {
		t.Fatalf("server-wide lock = %v", global)
	}
}

func TestSessionsListAndEndOthers(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "o.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	u, _ := st.CreateFirstUser(ctx, "admin", "hash")
	now := time.Unix(1_800_000_000, 0)
	m := &Sessions{Store: st, Now: func() time.Time { return now }}

	var cookies []*http.Cookie
	for range 3 {
		w := httptest.NewRecorder()
		if _, err := m.Start(ctx, w, u.ID); err != nil {
			t.Fatal(err)
		}
		cookies = append(cookies, w.Result().Cookies()[0])
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookies[1])

	list, err := m.List(r, 10)
	if err != nil || len(list) != 3 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	current := 0
	for _, s := range list {
		if s.Current {
			current++
		}
		if s.User != "admin" || !s.Created.Equal(now) || !s.Expires.Equal(now.Add(SessionTTL)) {
			t.Errorf("session = %+v", s)
		}
	}
	if current != 1 {
		t.Fatalf("%d sessions marked current, want 1", current)
	}

	if n, err := m.EndOthers(r); err != nil || n != 2 {
		t.Fatalf("EndOthers = %d, %v", n, err)
	}
	if _, err := m.Lookup(r); err != nil {
		t.Fatalf("own session ended too: %v", err)
	}
	other := httptest.NewRequest("GET", "/", nil)
	other.AddCookie(cookies[0])
	if _, err := m.Lookup(other); err != store.ErrNotFound {
		t.Fatalf("another session survived: %v", err)
	}
	if _, err := m.EndOthers(httptest.NewRequest("GET", "/", nil)); err != store.ErrNotFound {
		t.Fatalf("EndOthers without a session = %v, want ErrNotFound", err)
	}
}
