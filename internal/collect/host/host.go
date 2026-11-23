// Package host reads host figures from a (read-only, possibly bind-mounted) /proc and measures disk paths.
// Parsing is lenient: a missing or malformed field reads as zero, never as an error or a panic.
package host

import (
	"bytes"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"observarr/internal/collect"
)

// MaxFile is how much of any one file ReadFile reads.
const MaxFile = 64 << 10

// Read takes one reading from procRoot (e.g. "/host/proc"); Disks measures the disks. A missing procRoot is
// collect.ErrUnavailable; a missing or unreadable file within it leaves its fields zero.
func Read(procRoot string) (collect.Host, error) {
	if fi, err := os.Stat(procRoot); err != nil || !fi.IsDir() {
		return collect.Host{}, collect.ErrUnavailable
	}
	var h collect.Host
	read := func(name string) []byte {
		b, _ := ReadFile(filepath.Join(procRoot, name))
		return b
	}
	parseStat(read("stat"), &h)
	parseMeminfo(read("meminfo"), &h)
	if f := strings.Fields(string(read("loadavg"))); len(f) >= 3 {
		for i := range h.Load {
			h.Load[i] = parseFloat(f[i])
		}
	}
	if f := strings.Fields(string(read("uptime"))); len(f) >= 1 {
		if s := parseFloat(f[0]); s < float64(math.MaxInt64/int64(time.Second)) {
			h.Uptime = time.Duration(s * float64(time.Second)).Round(time.Millisecond)
		}
	}
	// Not "net/dev": /proc/net is /proc/self/net, Observarr's own network namespace. PID 1 is the host's init.
	for _, n := range NetDev(read("1/net/dev")) {
		if !virtual(n.Name) {
			h.Net = append(h.Net, n)
		}
	}
	return h, nil
}

// ReadFile reads at most MaxFile bytes of path. When the file is longer, the cut-off last line is dropped so a
// truncated number is never parsed as a smaller one.
func ReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFile {
		if i := bytes.LastIndexByte(b[:MaxFile], '\n'); i >= 0 {
			return b[:i+1], nil
		}
		return nil, errors.New("host: " + path + ": line too long")
	}
	return b, nil
}

// NetDev parses a /proc/net/dev table (all interfaces, in file order). Lines without "name:" are skipped.
func NetDev(data []byte) []collect.NetIf {
	var out []collect.NetIf
	for line := range strings.Lines(string(data)) {
		name, rest, ok := strings.Cut(line, ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			continue
		}
		f := strings.Fields(rest)
		out = append(out, collect.NetIf{Name: name, RxBytes: field(f, 0), TxBytes: field(f, 8)})
	}
	return out
}

// virtual reports interfaces that are not the host's own links: loopback and Docker/libvirt plumbing.
func virtual(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"veth", "docker", "br-", "virbr"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// parseStat reads the aggregate "cpu" line and counts the "cpuN" lines.
func parseStat(data []byte, h *collect.Host) {
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		if f[0] == "cpu" {
			// user nice system idle iowait irq softirq steal [guest guest_nice]; guest time is already counted in
			// user and nice, so it is left out of the total.
			v := f[1:]
			for i := range 8 {
				h.CPUTotal += field(v, i)
			}
			h.CPUIdle = field(v, 3) + field(v, 4)
		} else if _, err := strconv.Atoi(f[0][3:]); err == nil {
			h.CPUs++
		}
	}
}

// parseMeminfo reads the four memory fields it needs, converting kB to bytes.
func parseMeminfo(data []byte, h *collect.Host) {
	dst := map[string]*uint64{
		"MemTotal": &h.MemTotal, "MemAvailable": &h.MemAvailable,
		"SwapTotal": &h.SwapTotal, "SwapFree": &h.SwapFree,
	}
	for line := range strings.Lines(string(data)) {
		key, rest, ok := strings.Cut(line, ":")
		p := dst[key]
		if !ok || p == nil {
			continue
		}
		if kb := field(strings.Fields(rest), 0); kb <= math.MaxUint64>>10 {
			*p = kb << 10
		}
	}
}

// field returns f[i] as an unsigned number, or 0 when it is missing or not one.
func field(f []string, i int) uint64 {
	if i >= len(f) {
		return 0
	}
	n, err := strconv.ParseUint(f[i], 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseFloat returns s as a finite, non-negative number, or 0.
func parseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
