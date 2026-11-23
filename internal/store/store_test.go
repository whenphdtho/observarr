package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observarr.db")
	s, err := Open(context.Background(), path, quiet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func migrationCount(t *testing.T) int {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

func TestOpenCreatesSchema(t *testing.T) {
	s, _ := openTemp(t)
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != migrationCount(t) {
		t.Fatalf("schema version %d, want %d", version, migrationCount(t))
	}
	if _, err := s.db.Exec("SELECT id, name, password_hash FROM users"); err != nil {
		t.Fatalf("users table missing: %v", err)
	}
}

func TestMigrationNamesAreNumberedInOrder(t *testing.T) {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range names {
		want := fmt.Sprintf("migrations/%04d_", i+1)
		if !strings.HasPrefix(n, want) {
			t.Errorf("migration %d is %s, want prefix %s", i+1, n, want)
		}
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	s, path := openTemp(t)
	if _, err := s.db.Exec("PRAGMA user_version = 999"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := Open(context.Background(), path, quiet); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("expected a 'newer schema' error, got %v", err)
	}
}

func TestCorruptDatabaseIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observarr.db")
	junk := []byte("this is not a database file at all, just text")
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path, quiet); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Open of a corrupt file = %v, want ErrCorrupt", err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(junk) {
		t.Fatal("a corrupt database must be left exactly as it is")
	}

	moved, err := MoveAside(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(moved); err != nil || fi.Mode().Perm() != 0o600 || !strings.HasPrefix(moved, path+".corrupt-") {
		t.Fatalf("moved to %s: %v %v", moved, fi, err)
	}
	s, err := Open(context.Background(), path, quiet)
	if err != nil {
		t.Fatalf("Open after moving the corrupt file aside: %v", err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != migrationCount(t) {
		t.Fatalf("schema version %d, want %d", version, migrationCount(t))
	}
}

func TestOpenPathWithURLMetacharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a?b#c%d")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "observarr.db")
	s, err := Open(context.Background(), path, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file must exist at %s: %v", path, err)
	}
}

func TestPermissionErrorIsNotTreatedAsCorruption(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permission bits")
	}
	s, path := openTemp(t)
	s.Close()
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o600) // allow TempDir cleanup

	if _, err := Open(context.Background(), path, quiet); err == nil {
		t.Fatal("expected an error opening a database without read permission")
	}
	if moved, _ := filepath.Glob(path + ".corrupt-*"); len(moved) != 0 {
		t.Fatalf("a permission error must not move the database aside, found %v", moved)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the original database file must still be in place: %v", err)
	}
}

func TestCancelledContextIsNotTreatedAsCorruption(t *testing.T) {
	s, path := openTemp(t)
	s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Open(ctx, path, quiet); err == nil {
		t.Fatal("expected an error opening with an already-cancelled context")
	}
	if moved, _ := filepath.Glob(path + ".corrupt-*"); len(moved) != 0 {
		t.Fatalf("a cancelled context must not move the database aside, found %v", moved)
	}
}

func TestBackupBeforeMigratingAnExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observarr.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec("CREATE TABLE leftover (a INTEGER)"); err != nil { // an existing file at schema 0
		t.Fatal(err)
	}
	old.Close()
	s, err := Open(context.Background(), path, quiet)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "observarr-*-pre-migrate-0.db"))
	if len(backups) != 1 {
		t.Fatalf("expected one pre-migration backup, found %v", backups)
	}
}
