package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestServicesEmptyIsNonNilSlice(t *testing.T) {
	s, _ := openTemp(t)
	list, err := s.Services(context.Background())
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if list == nil {
		t.Error("Services on an empty table returned nil, want a non-nil empty slice")
	}
	if len(list) != 0 {
		t.Errorf("len(Services()) = %d, want 0", len(list))
	}
}

func TestCreateServiceWithKeySealsBoundToNewID(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	var sealedWithID int64
	seal := func(id int64) ([]byte, error) {
		sealedWithID = id
		return []byte(fmt.Sprintf("sealed-bytes-for-%d", id)), nil
	}

	svc, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "Sonarr", BaseURL: "http://sonarr.local:8989"}, seal)
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	if svc.ID == 0 {
		t.Fatal("CreateService: ID is 0")
	}
	if sealedWithID != svc.ID {
		t.Errorf("seal was called with id %d, want %d", sealedWithID, svc.ID)
	}
	if svc.Type != "sonarr" || svc.Name != "Sonarr" || svc.BaseURL != "http://sonarr.local:8989" {
		t.Errorf("CreateService returned %+v", svc)
	}
	if len(svc.SealedKey) == 0 {
		t.Error("CreateService: SealedKey is empty, want the sealed bytes")
	}

	got, err := s.ServiceByID(ctx, svc.ID)
	if err != nil {
		t.Fatalf("ServiceByID: %v", err)
	}
	if string(got.SealedKey) != string(svc.SealedKey) {
		t.Errorf("stored SealedKey = %q, want %q", got.SealedKey, svc.SealedKey)
	}
}

func TestCreateServiceWithoutKey(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	seal := func(id int64) ([]byte, error) { return nil, nil }
	svc, err := s.CreateService(ctx, Service{Type: "prowlarr", Name: "Prowlarr", BaseURL: "http://prowlarr.local:9696"}, seal)
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}
	if svc.SealedKey != nil {
		t.Errorf("SealedKey = %v, want nil", svc.SealedKey)
	}

	got, err := s.ServiceByID(ctx, svc.ID)
	if err != nil {
		t.Fatalf("ServiceByID: %v", err)
	}
	if got.SealedKey != nil {
		t.Errorf("stored SealedKey = %v, want nil", got.SealedKey)
	}
}

func TestCreateServiceDuplicateNameReturnsErrExists(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	noKey := func(id int64) ([]byte, error) { return nil, nil }

	if _, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "Sonarr", BaseURL: "http://a:8989"}, noKey); err != nil {
		t.Fatalf("first CreateService: %v", err)
	}
	// Names are unique case-insensitively, matching the services.name COLLATE NOCASE column.
	_, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "SONARR", BaseURL: "http://b:8989"}, noKey)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate name: got %v, want ErrExists", err)
	}
}

func TestCreateServiceSealFailureRollsBackInsert(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	sealErr := errors.New("boom")
	seal := func(id int64) ([]byte, error) { return nil, sealErr }

	_, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "Sonarr", BaseURL: "http://a:8989"}, seal)
	if !errors.Is(err, sealErr) {
		t.Fatalf("CreateService with failing seal: got %v, want it to wrap %v", err, sealErr)
	}

	list, err := s.Services(ctx)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("Services after a failed seal = %v, want none (insert must have rolled back)", list)
	}
}

func TestServicesListOrderedByName(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	noKey := func(id int64) ([]byte, error) { return nil, nil }

	for _, name := range []string{"Zebra", "apple", "Mango"} {
		if _, err := s.CreateService(ctx, Service{Type: "sonarr", Name: name, BaseURL: "http://x:8989"}, noKey); err != nil {
			t.Fatalf("CreateService(%s): %v", name, err)
		}
	}

	list, err := s.Services(ctx)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("got %d services, want 3", len(list))
	}
	want := []string{"apple", "Mango", "Zebra"}
	for i, w := range want {
		if list[i].Name != w {
			t.Errorf("list[%d].Name = %q, want %q", i, list[i].Name, w)
		}
	}
}

func TestServiceByIDNotFound(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.ServiceByID(context.Background(), 12345); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestUpdateService(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	seal := func(id int64) ([]byte, error) { return []byte("key-v1"), nil }
	svc, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "Sonarr", BaseURL: "http://old:8989"}, seal)
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}

	update := Service{ID: svc.ID, Name: "Sonarr HD", BaseURL: "http://new:8989", SealedKey: []byte("key-v2")}
	if err := s.UpdateService(ctx, update); err != nil {
		t.Fatalf("UpdateService: %v", err)
	}

	got, err := s.ServiceByID(ctx, svc.ID)
	if err != nil {
		t.Fatalf("ServiceByID: %v", err)
	}
	if got.Name != "Sonarr HD" || got.BaseURL != "http://new:8989" || string(got.SealedKey) != "key-v2" {
		t.Errorf("ServiceByID after update = %+v", got)
	}
	if got.Type != "sonarr" {
		t.Errorf("Type changed by UpdateService: got %q, want sonarr", got.Type)
	}
}

func TestUpdateServiceCanClearKey(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	seal := func(id int64) ([]byte, error) { return []byte("key-v1"), nil }
	svc, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "Sonarr", BaseURL: "http://a:8989"}, seal)
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}

	update := Service{ID: svc.ID, Name: svc.Name, BaseURL: svc.BaseURL, SealedKey: nil}
	if err := s.UpdateService(ctx, update); err != nil {
		t.Fatalf("UpdateService: %v", err)
	}

	got, err := s.ServiceByID(ctx, svc.ID)
	if err != nil {
		t.Fatalf("ServiceByID: %v", err)
	}
	if got.SealedKey != nil {
		t.Errorf("SealedKey = %v, want nil after clearing", got.SealedKey)
	}
}

func TestUpdateServiceNotFound(t *testing.T) {
	s, _ := openTemp(t)
	err := s.UpdateService(context.Background(), Service{ID: 99999, Name: "Nope", BaseURL: "http://x"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestDeleteService(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	noKey := func(id int64) ([]byte, error) { return nil, nil }
	svc, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "Sonarr", BaseURL: "http://a:8989"}, noKey)
	if err != nil {
		t.Fatalf("CreateService: %v", err)
	}

	if err := s.DeleteService(ctx, svc.ID); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	if _, err := s.ServiceByID(ctx, svc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("ServiceByID after delete: got %v, want ErrNotFound", err)
	}
	if err := s.DeleteService(ctx, svc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteService again: got %v, want ErrNotFound", err)
	}
}

// TestMigration0002AppliesOnSchema1DatabaseWithBackup simulates upgrading a database left behind
// by 1a (schema 1, no services table) to 1b: migration 0002 must apply, and a pre-migration
// backup must be taken first, exactly like upgrading across any other migration boundary.
func TestMigration0002AppliesOnSchema1DatabaseWithBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observarr.db")

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	init, err := migrationFiles.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(string(init)); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = 1"); err != nil {
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
	if _, err := s.db.Exec("SELECT id, type, name, base_url, api_key, created_at, updated_at FROM services"); err != nil {
		t.Fatalf("services table missing: %v", err)
	}

	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "observarr-*-pre-migrate-1.db"))
	if len(backups) != 1 {
		t.Fatalf("expected one pre-migrate-1 backup, found %v", backups)
	}
}

// TestMigration0004KeepsServicesWithoutContainer upgrades a schema-3 database holding a service: the service keeps
// everything and has no container link, and a backup is taken first.
func TestMigration0004KeepsServicesWithoutContainer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "observarr.db")
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"migrations/0001_init.sql", "migrations/0002_services.sql", "migrations/0003_dismissed.sql"} {
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(string(body)); err != nil {
			t.Fatalf("applying %s: %v", name, err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO services (type, name, base_url, api_key, created_at, updated_at)
		VALUES ('sonarr', 'Sonarr', 'http://sonarr:8989', x'01', 1, 1); PRAGMA user_version = 3`); err != nil {
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
	svcs, err := s.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].Name != "Sonarr" || string(svcs[0].SealedKey) != "\x01" || svcs[0].Container != "" {
		t.Fatalf("services after migration = %+v", svcs)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "observarr-*-pre-migrate-3.db"))
	if len(backups) != 1 {
		t.Fatalf("expected one pre-migrate-3 backup, found %v", backups)
	}
}

func TestContainerLinkSetAndCleared(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	noKey := func(int64) ([]byte, error) { return nil, nil }
	svc, err := s.CreateService(ctx, Service{Type: "sonarr", Name: "Sonarr", BaseURL: "http://sonarr:8989",
		Container: "sonarr"}, noKey)
	if err != nil || svc.Container != "sonarr" {
		t.Fatalf("CreateService = %+v, %v", svc, err)
	}
	if got, _ := s.ServiceByID(ctx, svc.ID); got.Container != "sonarr" {
		t.Fatalf("stored container = %q", got.Container)
	}
	svc.Container = ""
	if err := s.UpdateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ServiceByID(ctx, svc.ID); got.Container != "" {
		t.Fatalf("container after clearing = %q", got.Container)
	}
}
