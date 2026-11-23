//go:build unix

package arr_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/adapters/arr"
)

// readConfigWithin reads dir as ConfigFolders does and fails the test if that does not return within 5 s.
func readConfigWithin(t *testing.T, dir string) (adapter.Candidate, bool, error) {
	t.Helper()
	type result struct {
		c   adapter.Candidate
		ok  bool
		err error
	}
	a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
	done := make(chan result, 1)
	go func() {
		c, ok, err := a.ReadConfig("sonarr", adapter.ConfigDir(dir))
		done <- result{c, ok, err}
	}()
	select {
	case r := <-done:
		return r.c, r.ok, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("ReadConfig hangs")
		return adapter.Candidate{}, false, nil
	}
}

func TestReadConfigRefusesFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "config.xml"), 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	// Nothing ever opens the FIFO's other end: a blocking open would wait forever.
	if _, ok, err := readConfigWithin(t, dir); ok || err == nil {
		t.Fatalf("ReadConfig of a FIFO = ok %v, err %v; want an error", ok, err)
	}
}

func TestReadConfigRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.xml")
	if err := os.WriteFile(target, []byte("<Config><ApiKey>k</ApiKey></Config>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "config.xml")); err != nil {
		t.Fatal(err)
	}
	// The message is shown to the user after the folder's name, and must not reveal the server's paths.
	if _, ok, err := readConfigWithin(t, dir); ok || err == nil || err.Error() != "config.xml is a symbolic link, not read" {
		t.Fatalf("ReadConfig of a symlinked config.xml = ok %v, err %v; want an error", ok, err)
	}
}

func TestReadConfigErrorNamesNoServerPath(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permission bits")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.xml"), nil, 0o000); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readConfigWithin(t, dir); err == nil || strings.Contains(err.Error(), dir) {
		t.Fatalf("ReadConfig of an unreadable config.xml = %v; want an error without %s", err, dir)
	}
}

// The app may swap config.xml for a FIFO or a link at any moment, also between a check and the open: no read may
// hang or follow the link.
func TestReadConfigSurvivesSwaps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.xml")
	secret := filepath.Join(t.TempDir(), "secret.xml")
	if err := os.WriteFile(secret, []byte("<Config><ApiKey>stolen</ApiKey></Config>"), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	swapped := make(chan struct{})
	go func() {
		defer close(swapped)
		tmp := filepath.Join(dir, "next")
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			switch i % 3 {
			case 0:
				_ = os.WriteFile(tmp, []byte(sonarrConfigXML("")), 0o600)
			case 1:
				_ = syscall.Mkfifo(tmp, 0o600)
			case 2:
				_ = os.Symlink(secret, tmp)
			}
			_ = os.Rename(tmp, path)
		}
	}()
	defer func() { close(stop); <-swapped }()

	found := 0
	for range 300 {
		c, ok, _ := readConfigWithin(t, dir)
		if ok && c.APIKey == "stolen" {
			t.Fatal("followed a symbolic link")
		}
		if ok {
			found++
		}
	}
	t.Logf("%d of 300 reads found the regular file", found)
}
