package catalog_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/adapters/catalog"
	"observarr/internal/shape"
)

// hostileDeadline is how long every call to a misbehaving app may take; slack is what the test allows on top
// before it calls the call unbounded.
const (
	hostileDeadline = time.Second
	slack           = 2 * time.Second
)

// endless writes chunk to w until the client goes away or limit bytes are written.
func endless(w http.ResponseWriter, r *http.Request, head, chunk string, limit int) {
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, head)
	for n := len(head); n < limit && r.Context().Err() == nil; n += len(chunk) {
		if _, err := io.WriteString(w, chunk); err != nil {
			return
		}
	}
}

// gzipBomb is 256 MiB of spaces, gzipped to about 250 KiB.
var gzipBomb = sync.OnceValue(func() []byte {
	var b bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
	zw.Write([]byte("["))
	spaces := bytes.Repeat([]byte(" "), 1<<20)
	for range 256 {
		zw.Write(spaces)
	}
	zw.Write([]byte("]"))
	zw.Close()
	return b.Bytes()
})

// readBodyFirst reads a request's body before h answers: only then does the server notice a client that leaves,
// which ends the request's context.
func readBodyFirst(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		h(w, r)
	}
}

func body(contentType, s string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, s)
	}
}

// hostileApps each misbehave in one way. mustFail marks those no adapter can take as an answer. An endless list is
// not among them: an adapter that counts a long list reports the part it read as capped.
var hostileApps = []struct {
	name     string
	mustFail bool
	handler  http.HandlerFunc
}{
	{"slow drip", true, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rc := http.NewResponseController(w)
		for _, c := range []byte(`{"version":"1","records":[]}`) {
			w.Write([]byte{c})
			rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(time.Second):
			}
		}
	}},
	{"never answers", true, func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }},
	{"huge body", true, func(w http.ResponseWriter, r *http.Request) {
		endless(w, r, `{"a":"`, strings.Repeat("x", 64<<10), 256<<20)
	}},
	{"huge array", false, func(w http.ResponseWriter, r *http.Request) {
		endless(w, r, `[`, strings.Repeat(`{"a":"`+strings.Repeat("x", 1000)+`"},`, 64), 256<<20)
	}},
	{"gzip bomb", false, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(gzipBomb())
	}},
	{"wrong content type", false, body("image/png", `{}`)},
	{"login page", true, body("text/html", `<!doctype html><html><form action="/login"><input name="password"></form></html>`)},
	{"redirect loop", true, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.RequestURI(), http.StatusFound)
	}},
	{"redirect elsewhere", true, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://198.51.100.1/login", http.StatusFound)
	}},
	{"invalid UTF-8", false, body("application/json",
		"{\"version\":\"\xff\xfe\",\"appName\":\"\xc3\",\"records\":[{\"title\":\"\xed\xa0\x80\"}],\"result\":\"\xff\"}")},
	{"deeply nested", true, func(w http.ResponseWriter, r *http.Request) {
		endless(w, r, "", strings.Repeat("[", 4096), 8<<20)
	}},
	{"numbers out of range", false, body("application/json",
		`{"version":1e400,"totalRecords":99999999999999999999999,"records":[{"size":-1e400,"sizeleft":1e308}],`+
			`"result":[1e400],"mb":"1e400"}`)},
	{"negative sizes", false, body("application/json",
		`{"totalRecords":-5,"records":[{"size":-1,"sizeleft":-2,"progress":-3}],"result":{"bytes":-1,"speed":-1}}`)},
	{"null", false, body("application/json", `null`)},
	{"empty array", false, body("application/json", `[]`)},
	{"empty object", false, body("application/json", `{}`)},
	{"null fields", false, body("application/json",
		`{"records":null,"result":null,"queue":null,"slots":null,"pageInfo":null,"results":null,"version":null}`)},
	{"server error", true, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, strings.Repeat("e", 8<<20), http.StatusInternalServerError)
	}},
}

// TestHostileApps runs every adapter against every misbehaving app: each call ends within its deadline, never
// panics, gives an error for what is no answer at all, and whatever it takes as an answer is a sound reading.
func TestHostileApps(t *testing.T) {
	if testing.Short() {
		t.Skip("talks to slow fake apps")
	}
	r := adapter.NewRegistry()
	catalog.Register(r)
	for _, app := range hostileApps {
		t.Run(app.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(readBodyFirst(app.handler))
			t.Cleanup(ts.Close)
			u, _ := url.Parse(ts.URL)
			client := adapter.NewHTTPClient()
			t.Cleanup(client.CloseIdleConnections)
			for _, info := range r.Infos() {
				t.Run(info.Type, func(t *testing.T) {
					t.Parallel()
					a, _ := r.Get(info.Type)
					tgt := adapter.Target{BaseURL: u, APIKey: "user:secret", HTTP: client}
					eachCall(t, a, tgt, func(call string, data any, err error, took time.Duration) {
						if took > hostileDeadline+slack {
							t.Errorf("%s took %v", call, took)
						}
						if app.mustFail && err == nil {
							t.Errorf("%s took it as an answer: %+v", call, data)
						}
						if err != nil && strings.Contains(err.Error(), "secret") {
							t.Errorf("%s error holds the key: %v", call, err)
						}
						if kind, ok := strings.CutPrefix(call, "probe "); ok && err == nil {
							checkReading(t, shape.Kind(kind), data)
						}
					})
				})
			}
		})
	}
}

// eachCall makes every call a can make to an app, each with hostileDeadline, and hands each result to check.
func eachCall(t *testing.T, a adapter.Adapter, tgt adapter.Target,
	check func(call string, data any, err error, took time.Duration)) {
	t.Helper()
	run := func(call string, f func(ctx context.Context) (any, error)) {
		ctx, cancel := context.WithTimeout(context.Background(), hostileDeadline)
		defer cancel()
		start := time.Now()
		data, err := f(ctx)
		check(call, data, err, time.Since(start))
	}
	run("detect", func(ctx context.Context) (any, error) { return a.Detect(ctx, tgt) })
	for _, kind := range a.Info().Kinds {
		run("probe "+string(kind), func(ctx context.Context) (any, error) { return a.Probe(ctx, tgt, kind) })
	}
	if f, ok := a.(adapter.Fingerprinter); ok {
		run("fingerprint", func(ctx context.Context) (any, error) {
			ok, v, err := f.Fingerprint(ctx, tgt)
			if err == nil && !ok {
				err = adapter.ErrWrongApp
			}
			return v, err
		})
	}
	if l, ok := a.(adapter.Linker); ok {
		run("linked apps", func(ctx context.Context) (any, error) { return l.LinkedApps(ctx, tgt) })
	}
}

// TestHostileAppsKeepMemoryBounded checks that what an adapter allocates for an endless or decompressing answer
// stays within a small multiple of the largest answer any adapter reads (adapter.MaxStreamBytes).
func TestHostileAppsKeepMemoryBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("reads large answers")
	}
	r := adapter.NewRegistry()
	catalog.Register(r)
	const bound = 4 * adapter.MaxStreamBytes
	for _, app := range hostileApps {
		switch app.name {
		case "huge body", "huge array", "gzip bomb":
		default:
			continue
		}
		ts := httptest.NewServer(readBodyFirst(app.handler))
		u, _ := url.Parse(ts.URL)
		client := adapter.NewHTTPClient()
		for _, info := range r.Infos() {
			a, _ := r.Get(info.Type)
			tgt := adapter.Target{BaseURL: u, APIKey: "k", HTTP: client}
			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			eachCall(t, a, tgt, func(call string, _ any, _ error, _ time.Duration) {
				runtime.ReadMemStats(&after)
				if grew := after.TotalAlloc - before.TotalAlloc; grew > bound {
					t.Errorf("%s: %s %s allocated %d MiB", app.name, info.Type, call, grew>>20)
				}
				before = after
			})
		}
		client.CloseIdleConnections()
		ts.Close()
	}
}
