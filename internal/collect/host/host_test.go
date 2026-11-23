package host

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"observarr/internal/collect"
)

func TestReadFixture(t *testing.T) {
	h, err := Read("testdata/proc")
	if err != nil {
		t.Fatal(err)
	}
	want := collect.Host{
		CPUTotal: 4705 + 356 + 584 + 3699 + 23 + 23 + 0 + 5, // user..steal; guest is already in user
		CPUIdle:  3699 + 23,
		CPUs:     4,
		Load:     [3]float64{0.52, 0.58, 0.59},
		MemTotal: 16318412 << 10, MemAvailable: 9764472 << 10,
		SwapTotal: 4194300 << 10, SwapFree: 4036092 << 10,
		Uptime: 350735*time.Second + 470*time.Millisecond,
		Net: []collect.NetIf{
			{Name: "eth0", RxBytes: 9123456789, TxBytes: 1234567890},
			{Name: "wlan0", RxBytes: 3000000, TxBytes: 1500000},
		},
	}
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("got  %+v\nwant %+v", h, want)
	}
}

func TestMissingRootIsUnavailable(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "nope")); !errors.Is(err, collect.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, err := Read(""); !errors.Is(err, collect.ErrUnavailable) {
		t.Fatalf("empty root: err = %v, want ErrUnavailable", err)
	}
}

// writeProc builds a fake proc root from name → content.
func writeProc(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestOddInputIsZeroNotError(t *testing.T) {
	root := writeProc(t, map[string]string{
		"stat":      "cpu  12 x\ncpu0\ncpuX 1 2\ncpu1 1 2 3 4",
		"loadavg":   "NaN +Inf 0.5e",
		"meminfo":   "MemTotal:\nMemAvailable: 99999999999999999999 kB\nSwapTotal: 18446744073709551615 kB\nSwapFree 12 kB\n:::\n",
		"uptime":    "1e300 1",
		"net/dev":   "ownns0: 1 2 3 4 5 6 7 8 9\n", // Observarr's own namespace: must not be read
		"1/net/dev": "garbage\n\n  eth0 1 2 3\n eth1: 5\nth2: 1 2 3 4 5 6 7 8 9\n:\n",
	})
	h, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	want := collect.Host{CPUTotal: 12, CPUs: 2, Net: []collect.NetIf{{Name: "eth1", RxBytes: 5}, {Name: "th2", RxBytes: 1, TxBytes: 9}}}
	if !reflect.DeepEqual(h, want) {
		t.Fatalf("got  %+v\nwant %+v", h, want)
	}
}

func TestMissingFilesAreZero(t *testing.T) {
	h, err := Read(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h, collect.Host{}) {
		t.Fatalf("got %+v, want zero", h)
	}
}

func TestReadFileCapsAndDropsPartialLine(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	root := writeProc(t, map[string]string{"big": strings.Repeat(line, 1000)}) // 100 000 bytes
	b, err := ReadFile(filepath.Join(root, "big"))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > MaxFile || len(b)%len(line) != 0 {
		t.Fatalf("len = %d: want ≤ %d and whole lines only", len(b), MaxFile)
	}
	if _, err := ReadFile(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing file: want an error")
	}
}

func TestNetDev(t *testing.T) {
	data := "Inter-|\n face |\n    lo: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0\neth0:1 0 0 0 0 0 0 0 2 0 0 0 0 0 0 0\n"
	got := NetDev([]byte(data))
	want := []collect.NetIf{{Name: "lo", RxBytes: 10, TxBytes: 20}, {Name: "eth0", RxBytes: 1, TxBytes: 2}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
