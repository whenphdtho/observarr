//go:build linux

package host

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStatfs(t *testing.T) {
	dir := t.TempDir()
	disks, stuck := NewDisks([]string{dir, filepath.Join(dir, "missing")}).Measure(context.Background())
	if len(disks) != 1 || len(stuck) != 0 {
		t.Fatalf("disks = %+v, stuck %v; want only %s", disks, stuck, dir)
	}
	d := disks[0]
	if d.Path != dir || d.Total == 0 || d.Free > d.Total || d.Avail > d.Free {
		t.Fatalf("implausible disk %+v", d)
	}
}
