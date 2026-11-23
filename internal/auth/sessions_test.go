package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"observarr/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "o.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newSessions(t *testing.T) (*Sessions, store.User) {
	t.Helper()
	st := newStore(t)
	u, err := st.CreateFirstUser(context.Background(), "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	return &Sessions{Store: st, Secure: true, Now: time.Now}, u
}

func TestSessionLifecycle(t *testing.T) {
	m, u := newSessions(t)
	w := httptest.NewRecorder()
	csrf, err := m.Start(context.Background(), w, u.ID)
	if err != nil || csrf == "" {
		t.Fatalf("Start = %q, %v", csrf, err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("Start set %d cookies, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != "__Host-observarr_session" || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie not hardened: %+v", cookie)
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(cookie)
	sess, err := m.Lookup(r)
	if err != nil || sess.UserName != "admin" || sess.CSRF != csrf {
		t.Fatalf("Lookup = %+v, %v", sess, err)
	}

	w = httptest.NewRecorder()
	if err := m.End(context.Background(), w, r); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Lookup(r); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after End: want ErrNotFound, got %v", err)
	}
	cookies = w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("End set %d cookies, want 1", len(cookies))
	}
	if cookies[0].MaxAge >= 0 {
		t.Fatal("End must expire the cookie")
	}
}

func TestLookupWithoutOrWithForgedCookie(t *testing.T) {
	m, _ := newSessions(t)
	r := httptest.NewRequest("GET", "/", nil)
	if _, err := m.Lookup(r); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("no cookie: want ErrNotFound, got %v", err)
	}
	r.AddCookie(&http.Cookie{Name: m.CookieName(), Value: "forged"})
	if _, err := m.Lookup(r); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("forged cookie: want ErrNotFound, got %v", err)
	}
}

func TestRevokeDeletesSessionWithoutCookie(t *testing.T) {
	m, u := newSessions(t)
	w := httptest.NewRecorder()
	if _, err := m.Start(context.Background(), w, u.ID); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(w.Result().Cookies()[0])
	if err := m.Revoke(context.Background(), r); err != nil { // takes no ResponseWriter: it cannot set a cookie
		t.Fatal(err)
	}
	if _, err := m.Lookup(r); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after Revoke: want ErrNotFound, got %v", err)
	}
	if err := m.Revoke(context.Background(), httptest.NewRequest("GET", "/", nil)); err != nil {
		t.Fatalf("Revoke without a cookie = %v, want nil", err)
	}
}

func TestInsecureCookieHasNoHostPrefix(t *testing.T) {
	m, u := newSessions(t)
	m.Secure = false // a browser drops a __Host- cookie without the Secure flag
	w := httptest.NewRecorder()
	if _, err := m.Start(context.Background(), w, u.ID); err != nil {
		t.Fatal(err)
	}
	c := w.Result().Cookies()[0]
	if c.Name != "observarr_session" || c.Secure || c.Path != "/" {
		t.Fatalf("insecure cookie = %+v", c)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	if _, err := m.Lookup(r); err != nil {
		t.Fatalf("Lookup = %v", err)
	}
}
