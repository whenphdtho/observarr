// Package local is the Collector that reads the host Observarr runs on: mounted /proc and cgroup trees and a
// Docker socket proxy. Each source is optional; a method whose source is not configured returns
// collect.ErrUnavailable.
package local

import (
	"context"
	"errors"
	"sync"

	"observarr/internal/collect"
	"observarr/internal/collect/cgroup"
	"observarr/internal/collect/docker"
	"observarr/internal/collect/host"
)

// usageReads bounds concurrent per-container cgroup reads.
const usageReads = 8

// Config names the sources; zero values mean "not configured".
type Config struct {
	ProcRoot   string   // host /proc, e.g. "/host/proc"
	CgroupRoot string   // host /sys/fs/cgroup, e.g. "/host/sys/fs/cgroup"
	Disks      []string // paths to measure
	Docker     *docker.Client
}

// Local implements collect.Collector.
type Local struct {
	cfg   Config
	disks *host.Disks
}

var _ collect.Collector = (*Local)(nil)

// New returns a collector over cfg's sources.
func New(cfg Config) *Local { return &Local{cfg: cfg, disks: host.NewDisks(cfg.Disks)} }

// Host reads the host figures. It waits at most about 2 s for disks that stopped answering.
func (l *Local) Host(ctx context.Context) (collect.Host, error) {
	if l.cfg.ProcRoot == "" {
		return collect.Host{}, collect.ErrUnavailable
	}
	h, err := host.Read(l.cfg.ProcRoot)
	if err == nil {
		h.Disks, h.StuckDisks = l.disks.Measure(ctx)
	}
	return h, err
}

// Containers lists containers from Docker.
func (l *Local) Containers(ctx context.Context) ([]collect.Container, error) {
	if l.cfg.Docker == nil {
		return nil, collect.ErrUnavailable
	}
	return l.cfg.Docker.Containers(ctx)
}

// Events streams Docker container events.
func (l *Local) Events(ctx context.Context) (<-chan collect.Event, error) {
	if l.cfg.Docker == nil {
		return nil, collect.ErrUnavailable
	}
	return l.cfg.Docker.Events(ctx)
}

// Usage reads every running container (PID > 0) and returns the readings that succeed, in cs order. A container
// that cannot be read is skipped: it may just have stopped. Setup problems come first: the error joins cgroup.ErrV1
// and cgroup.ErrCgroupNS (each once) if any container hit them; otherwise, if containers were to be read and none
// could be, it is the first failure, so a systematic problem (a wrong mount, say) does not look like an idle host.
func (l *Local) Usage(ctx context.Context, cs []collect.Container) ([]collect.ContainerUsage, error) {
	if l.cfg.ProcRoot == "" || l.cfg.CgroupRoot == "" {
		return nil, collect.ErrUnavailable
	}
	type result struct {
		u   collect.ContainerUsage
		err error
	}
	results := make([]*result, len(cs))
	sem := make(chan struct{}, usageReads)
	var wg sync.WaitGroup
	for i, c := range cs {
		if c.PID <= 0 {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			u, err := cgroup.Usage(l.cfg.ProcRoot, l.cfg.CgroupRoot, c.ID, c.PID)
			results[i] = &result{u, err}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var (
		out       []collect.ContainerUsage
		v1, ns    bool
		first     error
		setupErrs []error
	)
	for _, r := range results {
		switch {
		case r == nil:
		case r.err == nil:
			out = append(out, r.u)
		case errors.Is(r.err, cgroup.ErrV1):
			v1 = true
		case errors.Is(r.err, cgroup.ErrCgroupNS):
			ns = true
		case first == nil:
			first = r.err
		}
	}
	if v1 {
		setupErrs = append(setupErrs, cgroup.ErrV1)
	}
	if ns {
		setupErrs = append(setupErrs, cgroup.ErrCgroupNS)
	}
	if len(setupErrs) > 0 {
		return out, errors.Join(setupErrs...)
	}
	if len(out) == 0 {
		return nil, first
	}
	return out, nil
}
