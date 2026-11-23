package arr_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"observarr/internal/adapters/arr"
	"observarr/internal/shape"
)

// manyObjects is body's prefix, then empty objects up to almost 4 MiB, then suffix.
func manyObjects(prefix, suffix string) []byte {
	b := bytes.NewBufferString(prefix)
	for b.Len() < 4<<20-len(suffix)-8 {
		b.WriteString("{},")
	}
	b.WriteString("{}" + suffix)
	return b.Bytes()
}

// allocated returns the bytes f allocated.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestProbeHealthCapsWarningsWithoutReadingThemAll(t *testing.T) {
	body := manyObjects("[", "]")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/system/status", func(w http.ResponseWriter, r *http.Request) {
		w.Write(fixture(t, "sonarr_status.json"))
	})
	mux.HandleFunc("/api/v3/health", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	ts := httptest.NewServer(mux)
	defer ts.Close()

	var result any
	var err error
	alloc := allocated(func() {
		result, err = arr.New(definitionFor(t, "sonarr")).Probe(context.Background(), newTarget(t, ts.URL, "k"), shape.KindHealth)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(result.(shape.Health).Warnings); n != shape.MaxWarnings {
		t.Fatalf("%d warnings, want %d", n, shape.MaxWarnings)
	}
	t.Logf("%d bytes allocated", alloc)
	if alloc > 1<<20 {
		t.Fatalf("allocated %d bytes: the answer was read past the cap", alloc)
	}
}

func TestProbeQueueStopsAtCapWhenRecordsComeFirst(t *testing.T) {
	body := manyObjects(`{"records":[`, `],"totalRecords":5}`)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	ts := httptest.NewServer(mux)
	defer ts.Close()

	var result any
	var err error
	alloc := allocated(func() {
		result, err = arr.New(definitionFor(t, "sonarr")).Probe(context.Background(), newTarget(t, ts.URL, "k"), shape.KindQueue)
	})
	if err != nil {
		t.Fatal(err)
	}
	q := result.(shape.Queue)
	if len(q.Items) != shape.MaxQueueItems || q.Total != shape.MaxQueueItems {
		t.Fatalf("%d items, total %d; want %d of at least as many", len(q.Items), q.Total, shape.MaxQueueItems)
	}
	t.Logf("%d bytes allocated", alloc)
	if alloc > 1<<20 {
		t.Fatalf("allocated %d bytes: the answer was read past the cap", alloc)
	}
}

func TestProbeQueueReadsTotalAfterRecords(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"page":1,"records":[{"title":"a"}],"filters":[{"x":[1,{}]}],"TotalRecords":7}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	result, err := arr.New(definitionFor(t, "sonarr")).Probe(context.Background(), newTarget(t, ts.URL, "k"), shape.KindQueue)
	if err != nil {
		t.Fatal(err)
	}
	if q := result.(shape.Queue); q.Total != 7 || len(q.Items) != 1 || q.Items[0].Title != "a" {
		t.Fatalf("queue = %+v", q)
	}
}

func TestProbeQueueRejectsMalformed(t *testing.T) {
	for _, body := range []string{`[]`, `{"records":{}}`, `{"records":[1]}`, `{"totalRecords":"x"}`, `{"records":[{}]`} {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v3/queue", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) })
		ts := httptest.NewServer(mux)
		_, err := arr.New(definitionFor(t, "sonarr")).Probe(context.Background(), newTarget(t, ts.URL, "k"), shape.KindQueue)
		ts.Close()
		if err == nil {
			t.Errorf("%s: want an error", body)
		}
	}
}
