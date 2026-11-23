// Package cgroup reads one running container's cumulative usage from cgroup v2 files and its network namespace's
// /proc/<pid>/net/dev. The cgroup path comes from /proc/<pid>/cgroup and is confined to the cgroup root.
package cgroup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"observarr/internal/collect"
	"observarr/internal/collect/host"
)

// ErrV1 means the host has no (or only a hybrid) cgroup v2 hierarchy, so CPU and memory figures are not available.
var ErrV1 = errors.New("container CPU and memory need cgroup v2")

// ErrCgroupNS means the container's cgroup lies outside the cgroup root as Observarr sees it. That is what a private
// cgroup namespace (Docker's default on cgroup v2) looks like ("0::/../<id>"), and also what a hostile path would.
var ErrCgroupNS = errors.New("container usage needs cgroup: host on the Observarr container")

// Usage reads container id's usage via its main process pid, with procRoot and cgroupRoot being the host's /proc
// and /sys/fs/cgroup as mounted into Observarr. Missing counter files read as zero.
func Usage(procRoot, cgroupRoot string, id string, pid int) (collect.ContainerUsage, error) {
	u := collect.ContainerUsage{ID: id}
	if pid <= 0 {
		return u, fmt.Errorf("cgroup: container %s has no process", id)
	}
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
		return u, ErrV1 // v1 or hybrid: the unified tree's root has no controllers file
	}
	pidDir := filepath.Join(procRoot, strconv.Itoa(pid))
	b, err := host.ReadFile(filepath.Join(pidDir, "cgroup"))
	if err != nil {
		return u, fmt.Errorf("cgroup: process %d: %w", pid, err)
	}
	rel, found := "", false
	for line := range strings.Lines(string(b)) {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "0::"); ok {
			rel, found = p, true
			break
		}
	}
	if !found {
		return u, ErrV1
	}
	r, err := filepath.Rel(cgroupRoot, filepath.Join(cgroupRoot, rel)) // cleaned, relative to the root
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return u, ErrCgroupNS
	}
	// The container's cgroup is the first path component naming it ("docker-<id>.scope", "<id>"); the main process
	// may sit in a sub-cgroup below it. No such component: the PID was reused or the container list is stale.
	parts := strings.Split(r, string(filepath.Separator))
	i := slices.IndexFunc(parts, func(p string) bool { return id != "" && strings.Contains(p, id) })
	if i < 0 {
		return u, fmt.Errorf("cgroup: process %d is not in container %s", pid, id)
	}
	dir := filepath.Join(append([]string{cgroupRoot}, parts[:i+1]...)...)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return u, fmt.Errorf("cgroup: container %s: no cgroup directory", id)
	}

	read := func(name string) string {
		b, _ := host.ReadFile(filepath.Join(dir, name))
		return string(b)
	}
	for line := range strings.Lines(read("cpu.stat")) {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "usage_usec" {
			u.CPUUsec = number(f[1])
		}
	}
	u.MemBytes = number(strings.TrimSpace(read("memory.current")))
	u.MemLimit = number(strings.TrimSpace(read("memory.max"))) // "max" (unlimited) is not a number: 0
	for line := range strings.Lines(read("io.stat")) {
		for _, kv := range strings.Fields(line) { // "8:0 rbytes=… wbytes=… rios=…"
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "rbytes":
				u.BlkRead += number(v)
			case "wbytes":
				u.BlkWrite += number(v)
			}
		}
	}
	nd, _ := host.ReadFile(filepath.Join(pidDir, "net", "dev"))
	for _, n := range host.NetDev(nd) {
		if n.Name != "lo" {
			u.NetRx += n.RxBytes
			u.NetTx += n.TxBytes
		}
	}
	return u, nil
}

// number parses an unsigned decimal, or returns 0.
func number(s string) uint64 {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
