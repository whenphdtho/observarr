package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestDismissedEmptyIsNonNilMap(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.Dismissed(context.Background())
	if err != nil {
		t.Fatalf("Dismissed: %v", err)
	}
	if got == nil {
		t.Error("Dismissed on an empty table returned nil, want a non-nil empty map")
	}
	if len(got) != 0 {
		t.Errorf("len(Dismissed()) = %d, want 0", len(got))
	}
}

func TestDismissThenDismissed(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.Dismiss(ctx, "sonarr http://sonarr.local:8989/"); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if err := s.Dismiss(ctx, "radarr http://radarr.local:7878/"); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}

	got, err := s.Dismissed(ctx)
	if err != nil {
		t.Fatalf("Dismissed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(Dismissed()) = %d, want 2", len(got))
	}
	if !got["sonarr http://sonarr.local:8989/"] || !got["radarr http://radarr.local:7878/"] {
		t.Errorf("Dismissed() = %v, missing an expected key", got)
	}
}

func TestDismissIsIdempotent(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	key := "sonarr http://sonarr.local:8989/"

	if err := s.Dismiss(ctx, key); err != nil {
		t.Fatalf("first Dismiss: %v", err)
	}
	if err := s.Dismiss(ctx, key); err != nil {
		t.Fatalf("second Dismiss (same key): %v", err)
	}

	got, err := s.Dismissed(ctx)
	if err != nil {
		t.Fatalf("Dismissed: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(Dismissed()) = %d, want 1 (idempotent)", len(got))
	}
}

// TestMigration0003AppliesOnSchema2DatabaseWithBackup simulates upgrading a database left behind
// by 1b (schema 2, no dismissed table) to 1c: migration 0003 must apply, and a pre-migration
// backup must be taken first, exactly like upgrading across any other migration boundary (see
// TestMigration0002AppliesOnSchema1DatabaseWithBackup in services_test.go for the 1a→1b case this
// mirrors).
func TestMigration0003AppliesOnSchema2DatabaseWithBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observarr.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migrations/0001_init.sql", "migrations/0002_services.sql"} {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(string(body)); err != nil {
			t.Fatalf("applying %s: %v", name, err)
		}
	}
	if _, err := raw.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path, quiet)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != migrationCount(t) {
		t.Fatalf("schema version = %d, want %d", version, migrationCount(t))
	}
	if _, err := s.db.Exec("SELECT key, created_at FROM dismissed"); err != nil {
		t.Fatalf("dismissed table missing: %v", err)
	}

	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "observarr-*-pre-migrate-2.db"))
	if len(backups) != 1 {
		t.Fatalf("expected one pre-migrate-2 backup, found %v", backups)
	}
}
