package store

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestBackupsArePrivateAndOnlyTheNewestKept(t *testing.T) {
	s, path := openTemp(t)
	var made []string
	for i := range maxBackups + 2 {
		dst, err := s.Backup(context.Background(), "label-"+strconv.Itoa(i))
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, dst)
	}
	dir := filepath.Join(filepath.Dir(path), "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxBackups {
		t.Fatalf("%d files in backups, want %d", len(entries), maxBackups)
	}
	for _, dst := range made[:2] {
		if _, err := os.Stat(dst); err == nil {
			t.Errorf("old backup %s was kept", dst)
		}
	}
	for _, dst := range made[2:] {
		fi, err := os.Stat(dst)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("backup %s: %v %v, want mode 0600", dst, fi, err)
		}
	}
}

func TestFailedBackupLeavesNothing(t *testing.T) {
	s, path := openTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Backup(ctx, "x"); err == nil {
		t.Fatal("backup with a cancelled context succeeded")
	}
	if entries, _ := os.ReadDir(filepath.Join(filepath.Dir(path), "backups")); len(entries) != 0 {
		t.Fatalf("a failed backup left %v", entries)
	}
}

func TestBackupRemovesLeftoverTempFiles(t *testing.T) {
	s, path := openTemp(t)
	dir := filepath.Join(filepath.Dir(path), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	leftovers := []string{"observarr-20261101T000000.000000000Z-x.db.tmp", "observarr-20261101T000000.000000000Z-x.db.tmp-journal"}
	for _, f := range append(leftovers, "notes.tmp") {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Backup(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	for _, f := range leftovers {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s was left: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.tmp")); err != nil {
		t.Errorf("a file Observarr did not write was removed: %v", err)
	}
}

func TestProbe(t *testing.T) {
	s, path := openTemp(t)
	if err := s.Probe(context.Background()); err != nil {
		t.Fatalf("Probe on a writable database = %v", err)
	}
	if _, err := s.db.Exec("PRAGMA query_only = 1"); err != nil { // the one connection now refuses writes
		t.Fatal(err)
	}
	if err := s.Probe(context.Background()); Degraded(err) != "database is read-only" {
		t.Fatalf("Probe on a read-only database = %v (degraded %q)", err, Degraded(err))
	}
	s.Close()

	if os.Geteuid() == 0 {
		t.Skip("root ignores file permission bits")
	}
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if _, err := Open(context.Background(), path, quiet); Degraded(err) != "database is read-only" {
		t.Fatalf("Open in a read-only folder = %v (degraded %q)", err, Degraded(err))
	}
}

func TestDegradedIgnoresOtherErrors(t *testing.T) {
	if got := Degraded(context.Canceled); got != "" {
		t.Fatalf("Degraded(context.Canceled) = %q", got)
	}
	if got := Degraded(nil); got != "" {
		t.Fatalf("Degraded(nil) = %q", got)
	}
}

func TestSealedKeys(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if has, err := s.HasSealedKeys(ctx); err != nil || has {
		t.Fatalf("empty store: HasSealedKeys = %v, %v", has, err)
	}
	if _, err := s.CreateService(ctx, Service{Type: "t", Name: "keyless", BaseURL: "http://a"},
		func(int64) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasSealedKeys(ctx); has {
		t.Fatal("a service without key counts as a sealed key")
	}
	if _, err := s.CreateService(ctx, Service{Type: "t", Name: "keyed", BaseURL: "http://b"},
		func(int64) ([]byte, error) { return []byte("sealed"), nil }); err != nil {
		t.Fatal(err)
	}
	if has, _ := s.HasSealedKeys(ctx); !has {
		t.Fatal("HasSealedKeys = false with a sealed key saved")
	}
	if n, err := s.ClearSealedKeys(ctx); err != nil || n != 1 {
		t.Fatalf("ClearSealedKeys = %d, %v", n, err)
	}
	if has, _ := s.HasSealedKeys(ctx); has {
		t.Fatal("keys left after ClearSealedKeys")
	}
}

func TestPruneAudit(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	old := now.Add(-auditMaxAge - time.Hour).Unix()
	// maxAuditRows+10 recent rows and 3 that are too old.
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
		INSERT INTO audit (ts, actor, action) SELECT ?, 'a', 'x' || i FROM n`, maxAuditRows+10, now.Unix()); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := s.db.Exec("INSERT INTO audit (ts, actor, action) VALUES (?, 'a', 'old')", old); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PruneAudit(ctx, now); err != nil {
		t.Fatal(err)
	}
	var n, olds int
	var newest string
	s.db.QueryRow("SELECT count(*), sum(action = 'old') FROM audit").Scan(&n, &olds)
	s.db.QueryRow("SELECT action FROM audit WHERE action != 'old' ORDER BY id DESC LIMIT 1").Scan(&newest)
	if n != maxAuditRows || olds != 0 || newest != "x"+strconv.Itoa(maxAuditRows+10) {
		t.Fatalf("after pruning: %d rows, %d old, newest %q", n, olds, newest)
	}
}

func TestSessionsPerUserAreCapped(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	u, err := s.CreateFirstUser(ctx, "admin", "hash")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	for i := range maxSessionsPerUser + 5 {
		if err := s.CreateSession(ctx, []byte("h"+strconv.Itoa(i)), u.ID, "c", now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM sessions").Scan(&n)
	if n != maxSessionsPerUser {
		t.Fatalf("%d sessions kept, want %d", n, maxSessionsPerUser)
	}
	if _, err := s.SessionByToken(ctx, []byte("h0"), now); err == nil {
		t.Fatal("the oldest session must be the one dropped")
	}
	last := "h" + strconv.Itoa(maxSessionsPerUser+4)
	if _, err := s.SessionByToken(ctx, []byte(last), now); err != nil {
		t.Fatalf("newest session: %v", err)
	}
}
