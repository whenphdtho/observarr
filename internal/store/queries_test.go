package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFirstUserOnlyOnce(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if has, _ := s.HasUsers(ctx); has {
		t.Fatal("new database must have no users")
	}
	u, err := s.CreateFirstUser(ctx, "admin", "hash")
	if err != nil || u.ID == 0 || u.Name != "admin" {
		t.Fatalf("CreateFirstUser = %+v, %v", u, err)
	}
	if _, err := s.CreateFirstUser(ctx, "second", "hash"); !errors.Is(err, ErrExists) {
		t.Fatalf("second CreateFirstUser: want ErrExists, got %v", err)
	}
	got, err := s.UserByName(ctx, "ADMIN") // names are case-insensitive
	if err != nil || got.ID != u.ID {
		t.Fatalf("UserByName = %+v, %v", got, err)
	}
	if _, err := s.UserByName(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: want ErrNotFound, got %v", err)
	}
}

func TestEnsureUserCreatesPasswordlessUserOnce(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a, err := s.EnsureUser(ctx, "proxyuser")
	if err != nil || a.PasswordHash != "!" {
		t.Fatalf("EnsureUser = %+v, %v", a, err)
	}
	b, err := s.EnsureUser(ctx, "proxyuser")
	if err != nil || b.ID != a.ID {
		t.Fatalf("second EnsureUser = %+v, %v", b, err)
	}
}

func TestSessions(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	u, _ := s.CreateFirstUser(ctx, "admin", "hash")
	now := time.Unix(1_800_000_000, 0)
	if err := s.CreateSession(ctx, []byte("h1"), u.ID, "csrf1", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := s.SessionByToken(ctx, []byte("h1"), now)
	if err != nil || got.UserName != "admin" || got.CSRF != "csrf1" {
		t.Fatalf("SessionByToken = %+v, %v", got, err)
	}
	if _, err := s.SessionByToken(ctx, []byte("h1"), now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: want ErrNotFound, got %v", err)
	}
	if n, err := s.DeleteExpiredSessions(ctx, now.Add(2*time.Hour)); err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d, %v", n, err)
	}
	if err := s.CreateSession(ctx, []byte("h2"), u.ID, "csrf2", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession(ctx, []byte("h2")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByToken(ctx, []byte("h2"), now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session: want ErrNotFound, got %v", err)
	}
}

func TestAudit(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, err := s.Audit(ctx, "admin", "logged in", "from 192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAuditDetail(ctx, id, "from 192.0.2.2"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM audit WHERE actor = 'admin' AND action = 'logged in'
		AND detail = 'from 192.0.2.2'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
}
