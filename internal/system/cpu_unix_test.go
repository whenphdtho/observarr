//go:build unix

package system

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"syscall"
	"testing"
	"time"

	"observarr/internal/collect"
)

// TestSamplerCPU measures the process CPU time the sampler uses over a few seconds with 20 containers and someone
// watching (the 2 s interval). The collector is a fake, so this is the sampler's own cost, without file reads.
func TestSamplerCPU(t *testing.T) {
	if testing.Short() {
		t.Skip("takes 4 s")
	}
	f := &fake{host: collect.Host{CPUTotal: 1000, CPUIdle: 500, CPUs: 4, Net: []collect.NetIf{{Name: "eth0"}}}}
	for i := range 20 {
		id := fmt.Sprintf("%064x", i)
		f.cs = append(f.cs, collect.Container{ID: id, Name: fmt.Sprintf("app%02d", i), State: "running", PID: 100 + i,
			Labels: map[string]string{"com.docker.compose.project": "media"}})
		f.usage = append(f.usage, collect.ContainerUsage{ID: id, CPUUsec: uint64(i) * 1000, MemBytes: 1 << 28})
	}
	s := New(f, func() int { return 1 }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	before, start := cpuTime(t), time.Now()
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(4 * time.Second)
	cancel()
	<-done
	used, wall := cpuTime(t)-before, time.Since(start)

	percent := 100 * used.Seconds() / wall.Seconds()
	t.Logf("sampler CPU: %v in %v = %.3f %% of one core", used, wall.Round(time.Millisecond), percent)
	if percent >= 1 {
		t.Errorf("sampler uses %.3f %% of one core, want < 1 %%", percent)
	}
}

func cpuTime(t *testing.T) time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
