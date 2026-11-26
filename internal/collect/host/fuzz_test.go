package host

import (
	"math"
	"testing"

	"observarr/internal/collect"
)

func FuzzProc(f *testing.F) {
	f.Add([]byte("cpu  1 2 3 4 5 6 7 8 9 10\ncpu0 1 2 3 4\ncpu1\ncpux\n"))
	f.Add([]byte("MemTotal: 18446744073709551615 kB\nMemAvailable:\nSwapFree: -1 kB\n"))
	f.Add([]byte("NaN"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var h collect.Host
		parseStat(data, &h)
		parseMeminfo(data, &h)
		if v := parseFloat(string(data)); v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatalf("parseFloat(%q) = %v", data, v)
		}
	})
}
