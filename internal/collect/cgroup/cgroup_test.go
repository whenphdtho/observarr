package cgroup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"observarr/internal/collect"
)

const (
	idSystemd  = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	idCgroupfs = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"
)

func TestSystemdLayout(t *testing.T) {
	got, err := Usage("testdata/proc", "testdata/cgroup", idSystemd, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := collect.ContainerUsage{
		ID: idSystemd, CPUUsec: 81234567, MemBytes: 268435456, MemLimit: 1073741824,
		BlkRead: 104857600 + 1048576, BlkWrite: 52428800 + 2097152,
		NetRx: 52428800 + 1000, NetTx: 10485760 + 2000, // lo excluded
	}
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestCgroupfsLayout(t *testing.T) {
	got, err := Usage("testdata/proc", "testdata/cgroup", idCgroupfs, 200)
	if err != nil {
		t.Fatal(err)
	}
	want := collect.ContainerUsage{ID: idCgroupfs, CPUUsec: 5000, MemBytes: 1048576, NetRx: 52428800 + 1000, NetTx: 10485760 + 2000}
	if got != want { // memory.max "max" is 0 (unlimited); empty io.stat is 0
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestV1(t *testing.T) {
	if _, err := Usage("testdata/proc", "testdata/cgroup", idCgroupfs, 300); !errors.Is(err, ErrV1) {
		t.Fatalf("err = %v, want ErrV1", err)
	}
}

func TestEscapeRefused(t *testing.T) {
	// "0::/../etc" would reach testdata/etc/memory.current; "0::/../<id>" is how a private cgroup namespace looks.
	for pid, id := range map[int]string{400: "etc", 401: "etc", 601: idCgroupfs} {
		if _, err := Usage("testdata/proc", "testdata/cgroup", id, pid); !errors.Is(err, ErrCgroupNS) {
			t.Errorf("pid %d: err = %v, want ErrCgroupNS", pid, err)
		}
	}
}

func TestSubCgroupReadsContainerCgroup(t *testing.T) {
	// The main process sits in .../docker-<id>.scope/init (e.g. systemd inside the container): the container's
	// figures are the scope's, not init's (whose memory.current is 1).
	got, err := Usage("testdata/proc", "testdata/cgroup", idSystemd, 500)
	if err != nil {
		t.Fatal(err)
	}
	if got.MemBytes != 268435456 || got.CPUUsec != 81234567 {
		t.Fatalf("got %+v, want the scope's figures", got)
	}
}

func TestHybridIsV1(t *testing.T) {
	// A hybrid host has a "0::" line but no controllers in the unified tree (no cgroup.controllers at its root).
	if _, err := Usage("testdata/proc", t.TempDir(), idSystemd, 100); !errors.Is(err, ErrV1) {
		t.Fatalf("err = %v, want ErrV1", err)
	}
}

func TestOtherContainerRefused(t *testing.T) {
	// PID 100 belongs to idSystemd; a reused or stale PID must not report it as another container.
	for _, pid := range []int{100, 600} { // 600 is in the root cgroup "0::/"
		if _, err := Usage("testdata/proc", "testdata/cgroup", idCgroupfs, pid); err == nil || errors.Is(err, ErrCgroupNS) {
			t.Errorf("pid %d: err = %v, want an error for a PID whose cgroup is not the container's", pid, err)
		}
	}
}

func TestMissingFilesAreZero(t *testing.T) {
	proc, cg := t.TempDir(), v2Root(t)
	id := "abc123"
	mustWrite(t, filepath.Join(proc, "7", "cgroup"), "0::/docker/"+id+"\n")
	if err := os.MkdirAll(filepath.Join(cg, "docker", id), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Usage(proc, cg, id, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != (collect.ContainerUsage{ID: id}) {
		t.Fatalf("got %+v, want zero figures", got)
	}
}

func TestGoneIsError(t *testing.T) {
	proc, cg := t.TempDir(), v2Root(t)
	for _, pid := range []int{0, -1, 9} { // invalid, invalid, no such process
		if _, err := Usage(proc, cg, "x", pid); err == nil {
			t.Errorf("pid %d: want an error", pid)
		}
	}
	mustWrite(t, filepath.Join(proc, "7", "cgroup"), "0::/docker/x\n")
	if _, err := Usage(proc, cg, "x", 7); err == nil {
		t.Error("missing cgroup directory: want an error")
	}
}

func TestOddFiles(t *testing.T) {
	proc, cg := t.TempDir(), v2Root(t)
	id := "abc123"
	mustWrite(t, filepath.Join(proc, "7", "cgroup"), "garbage\n1:name=systemd:/x\n0::/docker/"+id) // no final newline
	dir := filepath.Join(cg, "docker", id)
	mustWrite(t, filepath.Join(dir, "cpu.stat"), "usage_usec\nusage_usec 99999999999999999999999\n")
	mustWrite(t, filepath.Join(dir, "memory.current"), "-5")
	mustWrite(t, filepath.Join(dir, "memory.max"), "")
	mustWrite(t, filepath.Join(dir, "io.stat"), "8:0 rbytes= wbytes=x =3 rbytes\n8:1 rbytes=7\n")
	got, err := Usage(proc, cg, id, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != (collect.ContainerUsage{ID: id, BlkRead: 7}) {
		t.Fatalf("got %+v", got)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// v2Root returns an empty cgroup v2 root.
func v2Root(t *testing.T) string {
	t.Helper()
	cg := t.TempDir()
	mustWrite(t, filepath.Join(cg, "cgroup.controllers"), "cpu io memory pids\n")
	return cg
}
