package store

import (
	"context"
	"testing"
	"time"
)

func TestAuditRows(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for _, r := range [][3]string{
		{"admin", "logged in", "from 192.0.2.1"},
		{"root", "login failed", "1 failed login from 203.0.113.5 in 15 min"},
		{"admin", "logged out", ""},
		{"(setup)", "setup token wrong", "2 wrong setup tokens from 203.0.113.6 in 15 min"},
	} {
		if _, err := s.Audit(ctx, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE audit SET ts = ts - 3 * 86400 WHERE actor = 'admin' AND action = 'logged in'"); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-24 * time.Hour)

	got, err := s.AuditRows(ctx, []string{"login failed", "setup token wrong"}, since, 10)
	if err != nil || len(got) != 2 || got[0].Actor != "(setup)" || got[1].Actor != "root" ||
		got[1].Detail != "1 failed login from 203.0.113.5 in 15 min" || got[1].At.IsZero() {
		t.Fatalf("failures = %+v, %v; want both, newest first", got, err)
	}
	if got, _ := s.AuditRows(ctx, []string{"login failed", "setup token wrong"}, since, 1); len(got) != 1 {
		t.Fatalf("limit 1 returned %d rows", len(got))
	}
	if got, _ := s.AuditRows(ctx, []string{"logged in"}, since, 10); len(got) != 0 {
		t.Fatalf("a row older than since was returned: %+v", got)
	}
	if got, _ := s.AuditRows(ctx, []string{"logged in"}, time.Time{}, 10); len(got) != 1 {
		t.Fatalf("logins since ever = %+v", got)
	}
	if got, err := s.AuditRows(ctx, nil, since, 10); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("no actions = %v, %v; want an empty list", got, err)
	}
}

func TestListAndEndOtherSessions(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	admin, _ := s.CreateFirstUser(ctx, "admin", "hash")
	other, _ := s.EnsureUser(ctx, "viewer")
	now := time.Unix(1_800_000_000, 0)
	for _, c := range []struct {
		hash string
		user int64
		exp  time.Duration
	}{{"old", admin.ID, -time.Hour}, {"a1", admin.ID, time.Hour}, {"a2", admin.ID, 2 * time.Hour}, {"v1", other.ID, 3 * time.Hour}} {
		if err := s.CreateSession(ctx, []byte(c.hash), c.user, "csrf-"+c.hash, now.Add(c.exp)); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.ListSessions(ctx, now, 10)
	if err != nil || len(list) != 3 || string(list[0].TokenHash) != "v1" || list[0].UserName != "viewer" ||
		string(list[2].TokenHash) != "a1" || !list[2].Expires.Equal(now.Add(time.Hour)) {
		t.Fatalf("ListSessions = %+v, %v", list, err)
	}
	if n, err := s.CountSessions(ctx, now); err != nil || n != 3 {
		t.Fatalf("CountSessions = %d, %v", n, err)
	}
	if n, err := s.DeleteSessionsExcept(ctx, []byte("a1"), now); err != nil || n != 2 {
		t.Fatalf("DeleteSessionsExcept = %d, %v; want the 2 other unexpired sessions ended", n, err)
	}
	list, _ = s.ListSessions(ctx, now, 10)
	if len(list) != 1 || string(list[0].TokenHash) != "a1" {
		t.Fatalf("left = %+v", list)
	}
}
