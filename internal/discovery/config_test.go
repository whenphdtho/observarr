package discovery

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"observarr/internal/adapter"
)

// onDisk writes m to a new temporary folder and returns its path.
func onDisk(t *testing.T, m fstest.MapFS) string {
	t.Helper()
	root := t.TempDir()
	for name, f := range m {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		var err error
		if f.Mode&fs.ModeSymlink != 0 {
			err = os.Symlink(string(f.Data), path)
		} else {
			err = os.WriteFile(path, f.Data, 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// stuckReader never returns from ReadConfig, like a read from a hung network mount.
type stuckReader struct {
	fakeApp
	release chan struct{}
}

func (r stuckReader) ReadConfig(string, fs.FS) (adapter.Candidate, bool, error) {
	<-r.release
	return adapter.Candidate{}, false, nil
}

func TestConfigFoldersStuckReadEndsWithTheRun(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	e, _ := newEngine(t, newStore(t), stuckReader{info("fake", 8989), release})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := e.Local(ctx, nil, onDisk(t, fstest.MapFS{"a/x": {}, "b/x": {}}))
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("run took %v after its context ended", d)
	}
	if len(res.Errors) != 1 || res.Errors[0] != (SourceError{"a", errNoAnswer}) {
		t.Fatalf("errors = %+v", res.Errors)
	}
}
