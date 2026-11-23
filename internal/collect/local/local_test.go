package local

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"observarr/internal/collect"
	"observarr/internal/collect/cgroup"
	"observarr/internal/collect/docker"
)

// The cgroup package's fixture tree: PIDs 100 and 200 are v2 containers, 300 is on cgroup v1.
const (
	proc = "../cgroup/testdata/proc"
	cg   = "../cgroup/testdata/cgroup"
	idA  = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	idB  = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

func TestNothingConfigured(t *testing.T) {
	l, ctx := New(Config{}), context.Background()
	if _, err := l.Host(ctx); !errors.Is(err, collect.ErrUnavailable) {
		t.Errorf("Host: %v", err)
	}
	if _, err := l.Containers(ctx); !errors.Is(err, collect.ErrUnavailable) {
		t.Errorf("Containers: %v", err)
	}
	if _, err := l.Events(ctx); !errors.Is(err, collect.ErrUnavailable) {
		t.Errorf("Events: %v", err)
	}
	if _, err := New(Config{ProcRoot: proc}).Usage(ctx, nil); !errors.Is(err, collect.ErrUnavailable) {
		t.Errorf("Usage without cgroup root: %v", err)
	}
}

func TestHost(t *testing.T) {
	h, err := New(Config{ProcRoot: "../host/testdata/proc"}).Host(context.Background())
	if err != nil || h.CPUs != 4 {
		t.Fatalf("host = %+v, %v", h, err)
	}
}

func TestUsage(t *testing.T) {
	l := New(Config{ProcRoot: proc, CgroupRoot: cg})
	cs := []collect.Container{
		{ID: idB, PID: 200}, {ID: "stopped"}, {ID: idA, PID: 100}, {ID: "gone", PID: 9},
	}
	us, err := l.Usage(context.Background(), cs)
	if err != nil {
		t.Fatal(err) // "gone" failing is not an error while others succeed
	}
	if len(us) != 2 || us[0].ID != idB || us[1].ID != idA || us[1].CPUUsec != 81234567 {
		t.Fatalf("usage = %+v", us)
	}
}

func TestUsageV1Once(t *testing.T) {
	l := New(Config{ProcRoot: proc, CgroupRoot: cg})
	cs := []collect.Container{{ID: idB, PID: 300}, {ID: idB, PID: 300}, {ID: idA, PID: 100}}
	us, err := l.Usage(context.Background(), cs)
	if !errors.Is(err, cgroup.ErrV1) || err.Error() != cgroup.ErrV1.Error() {
		t.Fatalf("err = %v, want ErrV1 once", err)
	}
	if len(us) != 1 || us[0].ID != idA {
		t.Fatalf("usage = %+v", us)
	}
}

func TestUsageAllFailing(t *testing.T) {
	l := New(Config{ProcRoot: proc, CgroupRoot: cg})
	us, err := l.Usage(context.Background(), []collect.Container{{ID: "x", PID: 9}, {ID: "y", PID: 10}})
	if err == nil || len(us) != 0 {
		t.Fatalf("usage = %+v, err = %v: want the failure reported", us, err)
	}
}

func TestUsageSetupErrorsFirst(t *testing.T) {
	// Observarr's own row ("0::/", PID 600) and a vanished PID fail first; the setup problems behind them
	// (v1 at PID 300, a private cgroup namespace at PID 601) must be what is reported.
	l := New(Config{ProcRoot: proc, CgroupRoot: cg})
	cs := []collect.Container{
		{ID: "observarr", PID: 600}, {ID: "gone", PID: 9}, {ID: idB, PID: 601}, {ID: idB, PID: 300}, {ID: idA, PID: 100},
	}
	us, err := l.Usage(context.Background(), cs)
	if !errors.Is(err, cgroup.ErrV1) || !errors.Is(err, cgroup.ErrCgroupNS) {
		t.Fatalf("err = %v, want ErrV1 and ErrCgroupNS", err)
	}
	if len(us) != 1 || us[0].ID != idA {
		t.Fatalf("usage = %+v", us)
	}
	us, err = l.Usage(context.Background(), []collect.Container{{ID: "observarr", PID: 600}, {ID: idB, PID: 601}})
	if !errors.Is(err, cgroup.ErrCgroupNS) || len(us) != 0 {
		t.Fatalf("usage = %+v, err = %v, want ErrCgroupNS", us, err)
	}
}

func TestUsageManyContainers(t *testing.T) {
	l := New(Config{ProcRoot: proc, CgroupRoot: cg})
	cs := make([]collect.Container, 50)
	for i := range cs {
		cs[i] = collect.Container{ID: idA, PID: 100}
	}
	us, err := l.Usage(context.Background(), cs)
	if err != nil || len(us) != len(cs) {
		t.Fatalf("got %d readings, %v", len(us), err)
	}
}

func TestDockerDelegation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			fmt.Fprint(w, `{"ApiVersion":"1.47"}`)
		case "/v1.47/containers/json":
			fmt.Fprint(w, `[{"Id":"`+idA+`","Names":["/one"],"State":"exited"}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	d, err := docker.New("tcp://" + srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cs, err := New(Config{Docker: d}).Containers(context.Background())
	if err != nil || len(cs) != 1 || cs[0].Name != "one" {
		t.Fatalf("containers = %+v, %v", cs, err)
	}
}
