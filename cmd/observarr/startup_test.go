package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"observarr/internal/config"
	"observarr/internal/store"
)

func testLog() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, nil)), &b
}

func TestCorruptDatabaseStopsStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observarr.db")
	junk := []byte("not a database, just text that is long enough to look like a file")
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	log, _ := testLog()
	_, err := openStore(context.Background(), path, false, log)
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "OBSERVARR_RESET_DB=1") {
		t.Fatalf("open of a corrupt database = %v, want an error naming the file and the way out", err)
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, junk) {
		t.Fatal("the corrupt database was changed")
	}

	log, logs := testLog()
	st, err := openStore(context.Background(), path, true, log)
	if err != nil {
		t.Fatalf("open with OBSERVARR_RESET_DB=1 = %v", err)
	}
	st.Close()
	moved, _ := filepath.Glob(path + ".corrupt-*")
	if len(moved) != 1 || !strings.Contains(logs.String(), "moved the corrupt database aside") {
		t.Fatalf("moved aside: %v; log: %s", moved, logs)
	}
}

func TestReadOnlyDataStopsStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permission bits")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	err := checkWritable(dir)
	uid := strconv.Itoa(os.Getuid())
	if err == nil || err.Error() != "data directory "+dir+" is not writable by uid "+uid+" — e.g. chown "+uid+" <host folder>" {
		t.Fatalf("read-only data directory = %v", err)
	}
}

func TestDataDirIsLockedAndPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	unlock, err := prepareDataDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("data directory mode = %04o, want 0700", fi.Mode().Perm())
	}
	if _, err := prepareDataDir(dir); err == nil || !strings.Contains(err.Error(), "in use by another Observarr") {
		t.Fatalf("second user of the data directory = %v", err)
	}
	unlock()
	unlock, err = prepareDataDir(dir)
	if err != nil {
		t.Fatalf("after the lock was released: %v", err)
	}
	unlock()
}

func TestMissingKeyWithSealedKeys(t *testing.T) {
	dir := t.TempDir()
	log, logs := testLog()
	st, err := store.Open(context.Background(), filepath.Join(dir, "observarr.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Config{SecretKeyFile: filepath.Join(dir, "secret.key")}
	if _, err := openBox(context.Background(), cfg, st, log); err != nil { // first start: the key is created
		t.Fatal(err)
	}
	if _, err := st.CreateService(context.Background(), store.Service{Type: "t", Name: "s", BaseURL: "http://a"},
		func(int64) ([]byte, error) { return []byte("sealed"), nil }); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cfg.SecretKeyFile); err != nil {
		t.Fatal(err)
	}

	_, err = openBox(context.Background(), cfg, st, log)
	if err == nil || !strings.Contains(err.Error(), cfg.SecretKeyFile) || !strings.Contains(err.Error(), "OBSERVARR_RESET_KEYS=1") {
		t.Fatalf("missing key with saved keys = %v", err)
	}
	if _, err := os.Stat(cfg.SecretKeyFile); !os.IsNotExist(err) {
		t.Fatal("a refused start must not create a new key")
	}

	cfg.ResetKeys = true
	if _, err := openBox(context.Background(), cfg, st, log); err != nil {
		t.Fatal(err)
	}
	if has, _ := st.HasSealedKeys(context.Background()); has || !strings.Contains(logs.String(), "dropped the saved API keys") {
		t.Fatalf("after OBSERVARR_RESET_KEYS=1: sealed keys left = %v; log: %s", has, logs)
	}
}

func TestSetupTokenOnlyBeforeSetup(t *testing.T) {
	dir := t.TempDir()
	log, logs := testLog()
	st, err := store.Open(context.Background(), filepath.Join(dir, "observarr.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	token, file, err := prepareSetup(context.Background(), st, dir, log)
	if err != nil || token == "" || file != filepath.Join(dir, "setup-token") {
		t.Fatalf("prepareSetup = %q %q %v", token, file, err)
	}
	if b, _ := os.ReadFile(file); string(b) != token+"\n" {
		t.Fatalf("token file holds %q", b)
	}
	if !strings.Contains(logs.String(), "level=WARN msg=\"setup token: "+token+" (also in "+file+")\"") {
		t.Fatalf("log: %s", logs)
	}

	if _, err := st.CreateFirstUser(context.Background(), "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if token, _, err := prepareSetup(context.Background(), st, dir, log); err != nil || token != "" {
		t.Fatalf("after setup: token %q, %v", token, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("a token file left from before setup must be removed")
	}
}
