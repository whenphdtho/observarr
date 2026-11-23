package host

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"observarr/internal/collect"
)

// fakeStatfs measures "/ok" and "/slow" at once, fails for "/gone" and blocks "/slow" while hang is set.
type fakeStatfs struct {
	mu    sync.Mutex
	hang  chan struct{} // nil: answer
	calls map[string]int
}

func (f *fakeStatfs) statfs(path string) (collect.Disk, error) {
	f.mu.Lock()
	f.calls[path]++
	hang := f.hang
	f.mu.Unlock()
	switch path {
	case "/gone":
		return collect.Disk{}, errors.New("no such file")
	case "/slow":
		if hang != nil {
			<-hang
		}
	}
	return collect.Disk{Path: path, Total: uint64(len(path))}, nil
}

func newFakeDisks(f *fakeStatfs, paths ...string) *Disks {
	d := NewDisks(paths)
	d.statfs, d.timeout = f.statfs, 50*time.Millisecond
	f.calls = map[string]int{}
	return d
}

func paths(ds []collect.Disk) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Path)
	}
	return out
}

func TestDisksKeepLastFiguresOfAStuckPath(t *testing.T) {
	f := &fakeStatfs{}
	d := newFakeDisks(f, "/slow", "/ok", "/gone")
	disks, stuck := d.Measure(context.Background())
	if !slices.Equal(paths(disks), []string{"/slow", "/ok"}) || stuck != nil {
		t.Fatalf("disks %v, stuck %v", paths(disks), stuck)
	}

	release := make(chan struct{})
	f.mu.Lock()
	f.hang = release
	f.mu.Unlock()
	start := time.Now()
	for range 3 {
		disks, stuck = d.Measure(context.Background())
		if !slices.Equal(paths(disks), []string{"/slow", "/ok"}) || !slices.Equal(stuck, []string{"/slow"}) {
			t.Fatalf("disks %v, stuck %v; want the last figures of /slow and it reported stuck", paths(disks), stuck)
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("three measurements took %v", d)
	}
	f.mu.Lock()
	if n := f.calls["/slow"]; n != 2 {
		t.Errorf("/slow measured %d times, want 2: never a second one while one is stuck", n)
	}
	f.hang = nil
	f.mu.Unlock()

	close(release)
	for range 100 {
		if _, stuck = d.Measure(context.Background()); stuck == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("/slow still reported stuck after it answered")
}

func TestDisksReturnAtContextEnd(t *testing.T) {
	f := &fakeStatfs{}
	d := newFakeDisks(f, "/slow")
	d.timeout = time.Hour
	f.hang = make(chan struct{})
	defer close(f.hang)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, stuck := d.Measure(ctx); !slices.Equal(stuck, []string{"/slow"}) {
		t.Fatalf("stuck = %v", stuck)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Measure took %v after its context ended", d)
	}
}
