package host

import (
	"context"
	"sync"
	"time"

	"observarr/internal/collect"
)

// diskTimeout is how long Measure waits for the disks.
const diskTimeout = 2 * time.Second

// Disks measures disk paths without ever waiting long for one that stopped answering, such as a hung network
// mount: each path is measured in a goroutine of its own, at most one at a time per path, and a path whose
// measurement is still running after the deadline keeps its last known figures.
type Disks struct {
	paths   []string
	timeout time.Duration
	statfs  func(path string) (collect.Disk, error)

	mu   sync.Mutex
	last map[string]collect.Disk // the latest figures of each path that could be measured
	busy map[string]bool         // paths being measured
}

// NewDisks returns a Disks for paths.
func NewDisks(paths []string) *Disks {
	return &Disks{paths: paths, timeout: diskTimeout, statfs: statfs,
		last: make(map[string]collect.Disk), busy: make(map[string]bool)}
}

// Measure measures every path, waiting at most 2 s and not past ctx's end. It returns the figures of each path
// that could be measured, latest or last known, in path order, and the paths that did not answer in time. A path
// that cannot be measured (e.g. a vanished mount) is left out.
func (d *Disks) Measure(ctx context.Context) (disks []collect.Disk, stuck []string) {
	done := make(chan struct{}, len(d.paths))
	started := 0
	d.mu.Lock()
	for _, p := range d.paths {
		if d.busy[p] {
			continue // still stuck since an earlier call
		}
		d.busy[p] = true
		started++
		go func() {
			disk, err := d.statfs(p)
			d.mu.Lock()
			delete(d.busy, p)
			if err != nil {
				delete(d.last, p)
			} else {
				d.last[p] = disk
			}
			d.mu.Unlock()
			done <- struct{}{}
		}()
	}
	d.mu.Unlock()

	timer := time.NewTimer(d.timeout)
	defer timer.Stop()
wait:
	for range started {
		select {
		case <-done:
		case <-timer.C:
			break wait
		case <-ctx.Done():
			break wait
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.paths {
		if d.busy[p] {
			stuck = append(stuck, p)
		}
		if disk, ok := d.last[p]; ok {
			disks = append(disks, disk)
		}
	}
	return disks, stuck
}
