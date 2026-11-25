package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSystemHistoryRangesAndSpark(t *testing.T) {
	e := newServicesEnv(t, withDocker(&fakeCollector{}))
	eventually(t, "the first sample", func() bool { return len(e.srv.System.Snapshot().Containers) == 1 })
	code, raw, _ := e.raw("GET", "/api/system", "", nil)
	var snap struct {
		Notices []string
		Spark   struct {
			Step         int
			Times        []int64
			CPU, MemUsed []*float64
		}
		PreviousCPU *float64 `json:"previousCpu"`
		PreviousMem *float64 `json:"previousMem"`
	}
	if code != 200 || json.Unmarshal(raw, &snap) != nil {
		t.Fatalf("GET /api/system = %d %s", code, raw)
	}
	if snap.Notices == nil || snap.Spark.Step != 60 || len(snap.Spark.Times) != 60 || len(snap.Spark.CPU) != 60 ||
		len(snap.Spark.MemUsed) != 60 || !strings.Contains(string(raw), `"previousCpu":null`) ||
		!strings.Contains(string(raw), `"previousMem":null`) {
		t.Fatalf("spark = %s", raw)
	}

	steps := map[string][2]int{"24h": {60, 1440}, "7d": {3600, 168}, "30d": {3600, 720}, "1y": {86400, 366}}
	for rng, want := range steps {
		code, raw, _ := e.raw("GET", "/api/system/history?range="+rng, "", nil)
		var v struct {
			Range        string
			Step         int
			Times        []int64
			CPU, MemUsed []*float64
		}
		if code != 200 || json.Unmarshal(raw, &v) != nil {
			t.Fatalf("GET /api/system/history?range=%s = %d %s", rng, code, raw)
		}
		if v.Range != rng || v.Step != want[0] || len(v.Times) != want[1] || len(v.CPU) != want[1] ||
			len(v.MemUsed) != want[1] || v.CPU[0] != nil {
			t.Fatalf("range %s: step %d, %d times, %d cpu, first %v", rng, v.Step, len(v.Times), len(v.CPU), v.CPU[0])
		}
	}
	code, raw, _ = e.raw("GET", "/api/system/history?range=5m", "", nil)
	if code != 200 || !strings.Contains(string(raw), `"range":"5m"`) {
		t.Fatalf("5m history = %d %s", code, raw)
	}
	// Without a range: the readings of the last five minutes, as before.
	if code, raw, _ := e.raw("GET", "/api/system/history", "", nil); code != 200 || !strings.HasPrefix(string(raw), "[") {
		t.Fatalf("history = %d %s", code, raw)
	}
}
