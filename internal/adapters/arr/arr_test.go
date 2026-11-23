package arr_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"observarr/internal/adapter"
	"observarr/internal/adapters/arr"
	"observarr/internal/shape"
)

// definitionFor returns the arr.Definitions entry for typ, failing the test if there is none.
func definitionFor(t *testing.T, typ string) arr.Definition {
	t.Helper()
	for _, d := range arr.Definitions {
		if d.Type == typ {
			return d
		}
	}
	t.Fatalf("no arr.Definitions entry for type %q", typ)
	return arr.Definition{}
}

// fixture reads a file from testdata.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// newTarget builds a Target pointing at the test server.
func newTarget(t *testing.T, serverURL, apiKey string) adapter.Target {
	t.Helper()
	base, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", serverURL, err)
	}
	return adapter.Target{BaseURL: base, APIKey: apiKey, HTTP: &http.Client{}}
}

func TestDetectSonarrOK(t *testing.T) {
	var gotPath, gotKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Api-Key")
		w.Write(fixture(t, "sonarr_status.json"))
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	id, err := a.Detect(context.Background(), newTarget(t, ts.URL, "test-key"))
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if id.Version != "4.0.9.2244" {
		t.Errorf("Version = %q, want 4.0.9.2244", id.Version)
	}
	if gotPath != "/api/v3/system/status" {
		t.Errorf("request path = %q, want /api/v3/system/status", gotPath)
	}
	if gotKey != "test-key" {
		t.Errorf("X-Api-Key = %q, want test-key", gotKey)
	}
}

func TestDetectRadarrAgainstSonarrIsWrongApp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "sonarr_status.json"))
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "radarr"))
	_, err := a.Detect(context.Background(), newTarget(t, ts.URL, "test-key"))
	if !errors.Is(err, adapter.ErrWrongApp) || err.Error() != "Sonarr answers at this address" {
		t.Errorf("got err %v, want ErrWrongApp naming Sonarr", err)
	}
	if phrase, _ := adapter.Describe(err); phrase != "Sonarr answers at this address" {
		t.Errorf("Describe = %q", phrase)
	}
}

func TestDetectHTMLPageIsWrongApp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write(fixture(t, "not_arr.html"))
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	_, err := a.Detect(context.Background(), newTarget(t, ts.URL, "test-key"))
	if !errors.Is(err, adapter.ErrWrongApp) {
		t.Errorf("got err %v, want ErrWrongApp", err)
	}
}

func TestDetectUnauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	_, err := a.Detect(context.Background(), newTarget(t, ts.URL, "wrong-key"))
	if !errors.Is(err, adapter.ErrUnauthorized) {
		t.Errorf("got err %v, want ErrUnauthorized", err)
	}
}

func TestDetect404OnStatusIsWrongApp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	_, err := a.Detect(context.Background(), newTarget(t, ts.URL, "test-key"))
	if !errors.Is(err, adapter.ErrWrongApp) {
		t.Errorf("got err %v, want ErrWrongApp", err)
	}
}

func TestDetectEmptyAppNameIsWrongApp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"version":"4.0.0.0"}`))
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	_, err := a.Detect(context.Background(), newTarget(t, ts.URL, "test-key"))
	if !errors.Is(err, adapter.ErrWrongApp) {
		t.Errorf("got err %v, want ErrWrongApp", err)
	}
}

func TestDetectNullBodyIsWrongApp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`null`))
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	_, err := a.Detect(context.Background(), newTarget(t, ts.URL, "test-key"))
	if !errors.Is(err, adapter.ErrWrongApp) {
		t.Errorf("got err %v, want ErrWrongApp", err)
	}
}

func TestDetectOldVersionIsUnsupported(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "sonarr_status_v2.json"))
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	_, err := a.Detect(context.Background(), newTarget(t, ts.URL, "test-key"))
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Errorf("got err %v, want ErrUnsupported", err)
	}
}

func TestDetectUnparsableVersionIsUnsupported(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"appName":"Sonarr","version":"vNext"}`))
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	_, err := a.Detect(context.Background(), newTarget(t, ts.URL, "test-key"))
	if !errors.Is(err, adapter.ErrUnsupported) {
		t.Errorf("got err %v, want ErrUnsupported", err)
	}
}

func TestProbeHealthMapsWarningsAndLevels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "sonarr_status.json"))
	})
	mux.HandleFunc("/api/v3/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "sonarr_health.json"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindHealth)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	health, ok := result.(shape.Health)
	if !ok {
		t.Fatalf("Probe returned %T, want shape.Health", result)
	}
	if health.Version != "4.0.9.2244" {
		t.Errorf("Version = %q, want 4.0.9.2244", health.Version)
	}
	if health.LatencyMS < 0 {
		t.Errorf("LatencyMS = %d, want >= 0", health.LatencyMS)
	}
	if health.Warnings == nil {
		t.Error("Warnings is nil, want a non-nil slice")
	}
	if len(health.Warnings) != 2 {
		t.Fatalf("len(Warnings) = %d, want 2", len(health.Warnings))
	}
	if health.Warnings[0].Level != shape.LevelWarning || health.Warnings[0].Source != "AppIndexerStatusCheck" {
		t.Errorf("Warnings[0] = %+v, want Level=warning Source=AppIndexerStatusCheck", health.Warnings[0])
	}
	if health.Warnings[1].Level != shape.LevelError || health.Warnings[1].Source != "ImportMechanismCheck" {
		t.Errorf("Warnings[1] = %+v, want Level=error Source=ImportMechanismCheck", health.Warnings[1])
	}
}

func TestProbeHealthEmptyWarningsIsNonNilSlice(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "sonarr_status.json"))
	})
	mux.HandleFunc("/api/v3/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[]`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindHealth)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	health := result.(shape.Health)
	if health.Warnings == nil || len(health.Warnings) != 0 {
		t.Errorf("Warnings = %#v, want a non-nil empty slice", health.Warnings)
	}
}

func TestProbeQueueMapsFieldsProgressProblem(t *testing.T) {
	var gotQuery, gotKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotKey = r.Header.Get("X-Api-Key")
		w.Write(fixture(t, "sonarr_queue.json"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindQueue)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	queue, ok := result.(shape.Queue)
	if !ok {
		t.Fatalf("Probe returned %T, want shape.Queue", result)
	}
	if queue.Total != 3 {
		t.Errorf("Total = %d, want 3", queue.Total)
	}
	if len(queue.Items) != 3 {
		t.Fatalf("len(Items) = %d, want 3", len(queue.Items))
	}
	if gotKey != "test-key" {
		t.Errorf("X-Api-Key = %q, want test-key", gotKey)
	}
	if !strings.Contains(gotQuery, "page=1") || !strings.Contains(gotQuery, "pageSize=50") ||
		!strings.Contains(gotQuery, "includeUnknownSeriesItems=true") {
		t.Errorf("query = %q, want page=1, pageSize=50 and includeUnknownSeriesItems=true", gotQuery)
	}

	downloading := queue.Items[0]
	if downloading.Status != "downloading" {
		t.Errorf("Items[0].Status = %q, want downloading", downloading.Status)
	}
	wantProgress := (2000000000.0 - 500000000.0) / 2000000000.0
	if downloading.Progress != wantProgress {
		t.Errorf("Items[0].Progress = %v, want %v", downloading.Progress, wantProgress)
	}
	if downloading.ETA != "00:45:00" {
		t.Errorf("Items[0].ETA = %q, want 00:45:00", downloading.ETA)
	}
	if downloading.Problem != "" {
		t.Errorf("Items[0].Problem = %q, want empty", downloading.Problem)
	}

	warning := queue.Items[1]
	if warning.Problem == "" {
		t.Error("Items[1].Problem is empty, want the joined status message")
	}
	if !strings.Contains(warning.Problem, "not imported") {
		t.Errorf("Items[1].Problem = %q, want it to mention the status message", warning.Problem)
	}

	zeroSize := queue.Items[2]
	if zeroSize.Progress != 0 {
		t.Errorf("Items[2].Progress = %v, want 0 (size is 0)", zeroSize.Progress)
	}
	if zeroSize.Status != "queued" {
		t.Errorf("Items[2].Status = %q, want queued", zeroSize.Status)
	}
}

func TestProbeQueueCapsAtMaxItems(t *testing.T) {
	const recordCount = shape.MaxQueueItems + 5
	var sb []byte
	sb = append(sb, []byte(fmt.Sprintf(`{"totalRecords":%d,"records":[`, recordCount))...)
	for i := 0; i < recordCount; i++ {
		if i > 0 {
			sb = append(sb, ',')
		}
		sb = append(sb, []byte(fmt.Sprintf(
			`{"title":"Item %d","status":"queued","size":100,"sizeleft":50,"timeleft":"","trackedDownloadStatus":"ok","statusMessages":[],"errorMessage":""}`,
			i))...)
	}
	sb = append(sb, []byte(`]}`)...)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Write(sb)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindQueue)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	queue := result.(shape.Queue)
	if queue.Total != recordCount {
		t.Errorf("Total = %d, want %d", queue.Total, recordCount)
	}
	if len(queue.Items) != shape.MaxQueueItems {
		t.Errorf("len(Items) = %d, want %d", len(queue.Items), shape.MaxQueueItems)
	}
}

func TestProbeQueueEmptyRecordsIsNonNilSlice(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"totalRecords":0,"records":[]}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindQueue)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	queue := result.(shape.Queue)
	if queue.Items == nil || len(queue.Items) != 0 {
		t.Errorf("Items = %#v, want a non-nil empty slice", queue.Items)
	}
}

func TestProbeQueueClampsProgressToUnitRange(t *testing.T) {
	body := `{"totalRecords":2,"records":[
		{"title":"Negative sizeleft","status":"downloading","size":100,"sizeleft":-50,"timeleft":"","trackedDownloadStatus":"ok","statusMessages":[],"errorMessage":""},
		{"title":"Sizeleft over size","status":"downloading","size":100,"sizeleft":150,"timeleft":"","trackedDownloadStatus":"ok","statusMessages":[],"errorMessage":""}
	]}`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindQueue)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	items := result.(shape.Queue).Items
	if len(items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(items))
	}
	if items[0].Progress != 1 {
		t.Errorf("Items[0].Progress (sizeleft < 0, raw > 1) = %v, want 1 (clamped)", items[0].Progress)
	}
	if items[1].Progress != 0 {
		t.Errorf("Items[1].Progress (sizeleft > size, raw < 0) = %v, want 0 (clamped)", items[1].Progress)
	}
}

func TestProbeQueueFractionalSizeDoesNotFailTheWholeQueue(t *testing.T) {
	body := `{"totalRecords":1,"records":[
		{"title":"Fractional","status":"downloading","size":100.0,"sizeleft":25.0,"timeleft":"","trackedDownloadStatus":"ok","statusMessages":[],"errorMessage":""}
	]}`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindQueue)
	if err != nil {
		t.Fatalf("Probe with a fractional size/sizeleft: %v", err)
	}
	items := result.(shape.Queue).Items
	if len(items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(items))
	}
	if items[0].SizeBytes != 100 || items[0].LeftBytes != 25 {
		t.Errorf("SizeBytes/LeftBytes = %d/%d, want 100/25", items[0].SizeBytes, items[0].LeftBytes)
	}
}

func TestProbeQueueBoundsOversizedTitle(t *testing.T) {
	big := strings.Repeat("t", 3*1024*1024)
	body := fmt.Sprintf(
		`{"totalRecords":1,"records":[{"title":%q,"status":"queued","size":0,"sizeleft":0,"timeleft":"","trackedDownloadStatus":"ok","statusMessages":[],"errorMessage":""}]}`,
		big)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindQueue)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	title := result.(shape.Queue).Items[0].Title
	if len(title) > shape.MaxText {
		t.Errorf("len(Title) = %d, want <= %d", len(title), shape.MaxText)
	}
	if !utf8.ValidString(title) {
		t.Error("Title is not valid UTF-8 after clipping")
	}
}

func TestProbeQueueBoundsOversizedProblem(t *testing.T) {
	big := strings.Repeat("p", 3*1024*1024)
	body := fmt.Sprintf(
		`{"totalRecords":1,"records":[{"title":"x","status":"queued","size":0,"sizeleft":0,"timeleft":"","trackedDownloadStatus":"ok","statusMessages":[],"errorMessage":%q}]}`,
		big)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindQueue)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	problem := result.(shape.Queue).Items[0].Problem
	if len(problem) > shape.MaxText {
		t.Errorf("len(Problem) = %d, want <= %d", len(problem), shape.MaxText)
	}
	if !utf8.ValidString(problem) {
		t.Error("Problem is not valid UTF-8 after clipping")
	}
}

func TestProbeHealthBoundsOversizedMessage(t *testing.T) {
	big := strings.Repeat("m", 3*1024*1024)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "sonarr_status.json"))
	})
	mux.HandleFunc("/api/v3/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(fmt.Sprintf(`[{"source":"Check","type":"warning","message":%q}]`, big)))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	result, err := a.Probe(context.Background(), newTarget(t, ts.URL, "test-key"), shape.KindHealth)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	msg := result.(shape.Health).Warnings[0].Message
	if len(msg) > shape.MaxText {
		t.Errorf("len(Message) = %d, want <= %d", len(msg), shape.MaxText)
	}
	if !utf8.ValidString(msg) {
		t.Error("Message is not valid UTF-8 after clipping")
	}
}

func TestProwlarrInfoHasNoQueueKindAndProbeQueueIsUnknownKind(t *testing.T) {
	a := arr.New(definitionFor(t, "prowlarr"))
	info := a.Info()
	for _, k := range info.Kinds {
		if k == shape.KindQueue {
			t.Fatalf("Prowlarr Info().Kinds = %v, want no queue kind", info.Kinds)
		}
	}

	_, err := a.Probe(context.Background(), adapter.Target{HTTP: &http.Client{}}, shape.KindQueue)
	if !errors.Is(err, adapter.ErrUnknownKind) {
		t.Errorf("got err %v, want ErrUnknownKind", err)
	}
}

func TestProbeUnknownKindReturnsErrUnknownKind(t *testing.T) {
	a := arr.New(definitionFor(t, "sonarr"))
	_, err := a.Probe(context.Background(), adapter.Target{HTTP: &http.Client{}}, shape.Kind("bogus"))
	if !errors.Is(err, adapter.ErrUnknownKind) {
		t.Errorf("got err %v, want ErrUnknownKind", err)
	}
}

func TestDetectWithBaseURLPathPrefix(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write(fixture(t, "sonarr_status.json"))
	}))
	defer ts.Close()

	base, err := url.Parse(ts.URL + "/sonarr")
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	a := arr.New(definitionFor(t, "sonarr"))
	_, err = a.Detect(context.Background(), adapter.Target{BaseURL: base, APIKey: "test-key", HTTP: &http.Client{}})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if gotPath != "/sonarr/api/v3/system/status" {
		t.Errorf("request path = %q, want /sonarr/api/v3/system/status", gotPath)
	}
}

func TestInfoBuildsFreshSlicesEachCall(t *testing.T) {
	a := arr.New(definitionFor(t, "sonarr"))
	first := a.Info()
	if len(first.Kinds) == 0 {
		t.Fatal("Info().Kinds is empty, want at least health")
	}
	first.Kinds[0] = "tampered"

	second := a.Info()
	if second.Kinds[0] == "tampered" {
		t.Error("mutating one Info() call's Kinds affected a later call")
	}
}

func TestHugeTextsAreClipped(t *testing.T) {
	huge := strings.Repeat("9", 3<<20) // 3 MiB, within GetJSON's 4 MiB body limit
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"appName":"Sonarr","version":"4.%s"}`, huge)
	})
	mux.HandleFunc("/api/v3/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"source":%q,"type":"warning","message":"m"}]`, huge)
	})
	// Over MaxText but, unlike huge, small enough that four of them in one response still fit
	// comfortably under GetJSON's 4 MiB body cap.
	big := strings.Repeat("x", 2000)
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"totalRecords":1,"records":[{"title":%q,"status":%q,"size":100,`+
			`"sizeleft":50,"timeleft":%q,"errorMessage":%q}]}`, big, big, big, big)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	a := arr.New(definitionFor(t, "sonarr"))
	target := newTarget(t, ts.URL, "test-key")

	id, err := a.Detect(context.Background(), target)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(id.Version) != shape.MaxText {
		t.Errorf("Identity.Version is %d bytes, want %d", len(id.Version), shape.MaxText)
	}
	result, err := a.Probe(context.Background(), target, shape.KindHealth)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	health := result.(shape.Health)
	if len(health.Version) != shape.MaxText || len(health.Warnings[0].Source) != shape.MaxText {
		t.Errorf("Health.Version is %d bytes, Warning.Source %d, want %d each",
			len(health.Version), len(health.Warnings[0].Source), shape.MaxText)
	}

	queueResult, err := a.Probe(context.Background(), target, shape.KindQueue)
	if err != nil {
		t.Fatalf("Probe(queue): %v", err)
	}
	item := queueResult.(shape.Queue).Items[0]
	if len(item.Title) != shape.MaxText || len(item.Status) != shape.MaxText ||
		len(item.ETA) != shape.MaxText || len(item.Problem) != shape.MaxText {
		t.Errorf("QueueItem lengths Title=%d Status=%d ETA=%d Problem=%d, want %d each",
			len(item.Title), len(item.Status), len(item.ETA), len(item.Problem), shape.MaxText)
	}
}
