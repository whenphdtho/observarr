// Package collect defines the read-only host and container data Observarr shows and the Collector
// interface behind which it is gathered, so a remote agent can be added later.
package collect

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Host is one host reading. Counters are cumulative; rates are computed by the sampler.
type Host struct {
	CPUTotal, CPUIdle                           uint64 // jiffies from /proc/stat "cpu" line (idle includes iowait)
	CPUs                                        int
	Load                                        [3]float64
	MemTotal, MemAvailable, SwapTotal, SwapFree uint64 // bytes
	Uptime                                      time.Duration
	Disks                                       []Disk
	StuckDisks                                  []string // paths that did not answer in time; Disks has their last known figures
	Net                                         []NetIf  // physical-ish interfaces (lo, veth*, docker*, br-* excluded)
}

// Disk is the capacity of one measured path, in bytes.
type Disk struct {
	Path  string `json:"path"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Avail uint64 `json:"avail"`
}

// NetIf is one network interface's cumulative byte counters.
type NetIf struct {
	Name             string
	RxBytes, TxBytes uint64
}

// Container is one container's metadata.
type Container struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	State        string            `json:"state"`
	Health       string            `json:"health"` // "" when no healthcheck
	StartedAt    time.Time         `json:"startedAt,omitzero"`
	RestartCount int               `json:"restartCount"`
	PID          int               `json:"-"`           // host PID of the main process, 0 when not running
	NetworkMode  string            `json:"networkMode"` // "bridge", "host", "container:<id>", a network name, …
	Ports        []Port            `json:"ports"`
	Labels       map[string]string `json:"labels"` // only keys starting with "observarr." or "com.docker.compose."
}

// OwnNetwork reports whether the container has a network namespace of its own, so that <proc>/<pid>/net/dev
// counts its own traffic: not with network mode "host" (the host's interfaces) or "container:<id>" (another
// container's).
func (c Container) OwnNetwork() bool {
	return c.NetworkMode != "host" && !strings.HasPrefix(c.NetworkMode, "container:")
}

// Port is one published or exposed container port.
type Port struct {
	Private int    `json:"private"`
	Public  int    `json:"public"` // 0 when not published
	Proto   string `json:"proto"`
}

// ContainerUsage is one cumulative stats reading for a running container.
type ContainerUsage struct {
	ID                 string
	CPUUsec            uint64 // cpu.stat usage_usec
	MemBytes, MemLimit uint64 // memory.current, memory.max (0 = unlimited)
	BlkRead, BlkWrite  uint64 // io.stat rbytes/wbytes summed
	NetRx, NetTx       uint64 // <proc>/<pid>/net/dev, lo excluded
}

// Event is a container lifecycle change.
type Event struct {
	ID, Name, Action string // Action: start, die, restart, health_status, destroy, …
	At               time.Time
}

// Collector reads host and container data. Each method may return ErrUnavailable when its source is not configured.
type Collector interface {
	Host(ctx context.Context) (Host, error)
	Containers(ctx context.Context) ([]Container, error)
	Usage(ctx context.Context, cs []Container) ([]ContainerUsage, error) // only running ones with PID > 0
	Events(ctx context.Context) (<-chan Event, error)                    // closed when ctx ends or the stream breaks
}

// ErrUnavailable is returned by a Collector method whose source is not configured.
var ErrUnavailable = errors.New("source not configured")
