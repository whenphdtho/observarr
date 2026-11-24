package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
	"observarr/internal/store"
)

const foundKey = "KEY-d1sc0very-never-show-9Qw"

// fakeApp is a test adapter; the optional interfaces are added by wrapping it.
type fakeApp struct{ info adapter.Info }

func (f fakeApp) Info() adapter.Info { return f.info }
func (fakeApp) Detect(context.Context, adapter.Target) (adapter.Identity, error) {
	return adapter.Identity{}, nil
}
func (fakeApp) Probe(context.Context, adapter.Target, shape.Kind) (any, error) { return nil, nil }

type fingerprinter struct {
	fakeApp
	fn func(ctx context.Context, t adapter.Target) (bool, string, error)
}

func (f fingerprinter) Fingerprint(ctx context.Context, t adapter.Target) (bool, string, error) {
	return f.fn(ctx, t)
}

type linker struct {
	fakeApp
	fn func(ctx context.Context, t adapter.Target) ([]adapter.Candidate, error)
}

func (l linker) LinkedApps(ctx context.Context, t adapter.Target) ([]adapter.Candidate, error) {
	return l.fn(ctx, t)
}

// reader recognises a folder holding app.conf, whose content is the key; a folder holding "bad"
// fails to read.
type reader struct{ fakeApp }

func (r reader) ReadConfig(name string, dir fs.FS) (adapter.Candidate, bool, error) {
	if _, err := fs.Stat(dir, "bad"); err == nil {
		return adapter.Candidate{}, false, errors.New("fake: app.conf is not valid")
	}
	b, err := fs.ReadFile(dir, "app.conf")
	if err != nil {
		return adapter.Candidate{}, false, nil
	}
	return adapter.Candidate{Type: r.info.Type, BaseURL: &url.URL{Scheme: "http", Host: ":8989", Path: "/"},
		APIKey: string(b), KeySource: "app.conf in " + name}, true, nil
}

func info(typ string, port int) fakeApp {
	return fakeApp{adapter.Info{Type: typ, Name: strings.ToUpper(typ[:1]) + typ[1:], DefaultPort: port}}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "o.db"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// openKey stands in for the credential box: a test service's sealed key is the key itself.
func openKey(svc store.Service) (string, error) { return string(svc.SealedKey), nil }

// saveService stores a service of type typ at rawURL with key ("" for none).
func saveService(t *testing.T, st *store.Store, typ, name, rawURL, key string) {
	t.Helper()
	if _, err := st.CreateService(context.Background(), store.Service{Type: typ, Name: name, BaseURL: rawURL},
		func(int64) ([]byte, error) { return []byte(key), nil }); err != nil {
		t.Fatal(err)
	}
}

func newEngine(t *testing.T, st *store.Store, adapters ...adapter.Adapter) (*Engine, *clock) {
	t.Helper()
	reg := adapter.NewRegistry()
	for _, a := range adapters {
		reg.Register(a)
	}
	c := &clock{t: time.Unix(1_700_000_000, 0)}
	return New(reg, nil, st, openKey, c.now, slog.New(slog.NewTextHandler(io.Discard, nil))), c
}

// okFor recognises exactly the given host names.
func okFor(hosts ...string) func(context.Context, adapter.Target) (bool, string, error) {
	return func(_ context.Context, t adapter.Target) (bool, string, error) {
		for _, h := range hosts {
			if t.BaseURL.Hostname() == h {
				return true, "1.2", nil
			}
		}
		return false, "", nil
	}
}

func scan(t *testing.T, e *Engine, hosts ...string) Result {
	t.Helper()
	res, err := e.ScanNetwork(context.Background(), hosts)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestScanNetworkFinds(t *testing.T) {
	e, _ := newEngine(t, newStore(t), fingerprinter{info("fake", 1234), okFor("h2")})
	res := scan(t, e, "h1", "h2", "::1")
	if len(res.Suggestions) != 1 || len(res.Errors) != 0 {
		t.Fatalf("result = %+v", res)
	}
	s := res.Suggestions[0]
	if s.Type != "fake" || s.Name != "Fake" || s.URL != "http://h2:1234/" || !s.HostKnown || s.Version != "1.2" ||
		s.KeyFound || s.Source != "network" || len(s.ID) < 22 {
		t.Fatalf("suggestion = %+v", s)
	}
	if got := e.Suggestions(); len(got) != 1 || got[0] != s {
		t.Fatalf("stored = %+v", got)
	}
}

func TestScanNetworkProbesIPv6WithBrackets(t *testing.T) {
	var got atomic.Value
	e, _ := newEngine(t, newStore(t), fingerprinter{info("fake", 1234),
		func(_ context.Context, t adapter.Target) (bool, string, error) {
			got.Store(t.BaseURL.String())
			return false, "", nil
		}})
	scan(t, e, "fd00::1")
	if got.Load() != "http://[fd00::1]:1234/" {
		t.Fatalf("probed %v", got.Load())
	}
}

func TestScanNetworkBoundsConcurrency(t *testing.T) {
	var inFlight, peak atomic.Int32
	fn := func(context.Context, adapter.Target) (bool, string, error) {
		n := inFlight.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)
		return false, "", nil
	}
	// Two adapters on different ports: 2 × 150 probes.
	e, _ := newEngine(t, newStore(t), fingerprinter{info("a", 1), fn}, fingerprinter{info("b", 2), fn})
	hosts := make([]string, 150)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("h%d", i)
	}
	scan(t, e, hosts...)
	if p := peak.Load(); p > maxProbes || p < maxProbes/2 {
		t.Fatalf("peak in-flight probes = %d, want ≤ %d (and clearly concurrent)", p, maxProbes)
	}
}

func TestScanNetworkProbeTimeout(t *testing.T) {
	var budget atomic.Int64
	e, _ := newEngine(t, newStore(t), fingerprinter{info("slow", 1),
		func(ctx context.Context, _ adapter.Target) (bool, string, error) {
			d, _ := ctx.Deadline()
			budget.Store(int64(time.Until(d)))
			<-ctx.Done()
			return false, "", fmt.Errorf("fake: %w", ctx.Err())
		}})
	e.probeTimeout = 50 * time.Millisecond
	start := time.Now()
	res := scan(t, e, "h1", "h2")
	if time.Since(start) > time.Second || len(res.Suggestions) != 0 || len(res.Errors) != 0 {
		t.Fatalf("result after %v = %+v", time.Since(start), res)
	}
	if b := time.Duration(budget.Load()); b <= 0 || b > 50*time.Millisecond {
		t.Fatalf("probe deadline %v away, want ≤ 50ms", b)
	}
	if New(nil, nil, nil, nil, time.Now, nil).probeTimeout != 1500*time.Millisecond {
		t.Fatal("default probe timeout is not 1.5 s")
	}
}

func TestScanNetworkRunTimeout(t *testing.T) {
	var probes atomic.Int32
	e, _ := newEngine(t, newStore(t), fingerprinter{info("slow", 1),
		func(ctx context.Context, _ adapter.Target) (bool, string, error) {
			probes.Add(1)
			<-ctx.Done()
			return false, "", ctx.Err()
		}})
	e.scanTimeout = 100 * time.Millisecond
	hosts := make([]string, 500)
	for i := range hosts {
		hosts[i] = fmt.Sprintf("h%d", i)
	}
	start := time.Now()
	res := scan(t, e, hosts...)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("scan took %v, want it to stop after its 100ms deadline", d)
	}
	if n := probes.Load(); n > maxProbes {
		t.Fatalf("%d probes started, want only the first %d (the rest after the deadline)", n, maxProbes)
	}
	if len(res.Errors) != 1 || res.Errors[0] != (SourceError{Target: "network", Message: "time limit (120 s) reached — scan a smaller range or list hosts"}) {
		t.Fatalf("errors = %+v, want one saying the scan stopped", res.Errors)
	}
	if New(nil, nil, nil, nil, time.Now, nil).scanTimeout != 120*time.Second {
		t.Fatal("default scan timeout is not 120 s")
	}
	if maxProbes != 128 {
		t.Fatal("probes in flight are not capped at 128")
	}
}

func TestScanNetworkRefusedPortNotRetried(t *testing.T) {
	var second, otherPort atomic.Int32
	refused := fingerprinter{info("a", 80), func(context.Context, adapter.Target) (bool, string, error) {
		return false, "", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	}}
	samePort := fingerprinter{info("b", 80), func(context.Context, adapter.Target) (bool, string, error) {
		second.Add(1)
		return false, "", nil
	}}
	elsewhere := fingerprinter{info("c", 81), func(context.Context, adapter.Target) (bool, string, error) {
		otherPort.Add(1)
		return false, "", nil
	}}
	e, _ := newEngine(t, newStore(t), refused, samePort, elsewhere)
	res := scan(t, e, "h1")
	if second.Load() != 0 || otherPort.Load() != 1 || len(res.Errors) != 0 {
		t.Fatalf("same port probed %d times, other port %d times, errors %+v", second.Load(), otherPort.Load(),
			res.Errors)
	}
}

func TestScanNetworkFirstRecogniserWins(t *testing.T) {
	var later atomic.Int32
	e, _ := newEngine(t, newStore(t),
		fingerprinter{info("alpha", 80), okFor("h1")},
		fingerprinter{info("beta", 80), func(context.Context, adapter.Target) (bool, string, error) {
			later.Add(1)
			return true, "", nil
		}})
	res := scan(t, e, "h1")
	if len(res.Suggestions) != 1 || res.Suggestions[0].Type != "alpha" || later.Load() != 0 {
		t.Fatalf("result = %+v, later adapter asked %d times", res, later.Load())
	}
}

func TestScanNetworkErrorsAreShort(t *testing.T) {
	e, _ := newEngine(t, newStore(t), fingerprinter{info("fake", 80),
		func(_ context.Context, t adapter.Target) (bool, string, error) {
			if t.BaseURL.Hostname() == "nas" {
				return false, "", &net.DNSError{Err: "no such host", Name: "nas", IsNotFound: true}
			}
			return false, "", errors.New("body said " + foundKey)
		}})
	res := scan(t, e, "nas", "h2")
	want := []SourceError{{"h2:80", "request failed"}, {"nas:80", "host not found"}}
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %+v", res.Errors)
	}
	if res.Errors[0].Target == "nas:80" {
		res.Errors[0], res.Errors[1] = res.Errors[1], res.Errors[0]
	}
	if res.Errors[0] != want[0] || res.Errors[1] != want[1] {
		t.Fatalf("errors = %+v, want %+v", res.Errors, want)
	}
}

func TestLinked(t *testing.T) {
	var gotKey atomic.Value
	prowl := linker{info("hub", 9696), func(_ context.Context, t adapter.Target) ([]adapter.Candidate, error) {
		gotKey.Store(t.APIKey)
		return []adapter.Candidate{
			{Type: "fake", Name: "Shows", BaseURL: &url.URL{Scheme: "http", Host: "tv:8989"}},
			{Type: "ghost", Name: "Unknown", BaseURL: &url.URL{Scheme: "http", Host: "x:1"}},
		}, nil
	}}
	broken := linker{info("broken", 1), func(context.Context, adapter.Target) ([]adapter.Candidate, error) {
		return nil, fmt.Errorf("fake: %w", adapter.ErrUnauthorized)
	}}
	e, _ := newEngine(t, newStore(t), prowl, broken, info("fake", 8989))
	res, err := e.Linked(context.Background(), []LinkedSource{
		{Type: "hub", BaseURL: &url.URL{Scheme: "http", Host: "hub:9696"}, APIKey: "hub-key"},
		{Type: "fake", BaseURL: &url.URL{Scheme: "http", Host: "tv:8989"}}, // not a Linker
		{Type: "broken", BaseURL: &url.URL{Scheme: "http", Host: "b:1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey.Load() != "hub-key" {
		t.Fatalf("LinkedApps got key %v", gotKey.Load())
	}
	if len(res.Suggestions) != 1 || res.Suggestions[0].Type != "fake" || res.Suggestions[0].Name != "Shows" ||
		res.Suggestions[0].Source != "linked" || res.Suggestions[0].KeyFound {
		t.Fatalf("suggestions = %+v", res.Suggestions)
	}
	if len(res.Errors) != 1 || res.Errors[0] != (SourceError{"b:1", adapter.ErrUnauthorized.Error()}) {
		t.Fatalf("errors = %+v", res.Errors)
	}
}

func TestConfigFolders(t *testing.T) {
	root := fstest.MapFS{
		"sonarr/app.conf":   {Data: []byte(foundKey)},
		"sonarr4k/app.conf": {Data: []byte(foundKey)},
		"other/readme":      {Data: []byte("x")},
		"broken/bad":        {Data: []byte("x")},
		"linked":            {Mode: fs.ModeSymlink, Data: []byte("/etc")},
		"loose-file":        {Data: []byte("x")},
	}
	e, _ := newEngine(t, newStore(t), reader{info("fake", 8989)})
	res, err := e.Local(context.Background(), nil, onDisk(t, root))
	if err != nil {
		t.Fatal(err)
	}
	// Same app and port in two folders: both are suggested (the hosts may differ).
	if len(res.Suggestions) != 2 {
		t.Fatalf("suggestions = %+v", res.Suggestions)
	}
	s := res.Suggestions[0]
	if s.URL != "http://:8989/" || s.HostKnown || !s.KeyFound || s.Source != "config" || s.Name != "Fake" ||
		!strings.HasPrefix(s.KeySource, "app.conf in sonarr") {
		t.Fatalf("suggestion = %+v", s)
	}
	want := []SourceError{{"broken", "fake: app.conf is not valid"}, {"linked", "symbolic link, not read"}}
	if !slices.Equal(res.Errors, want) {
		t.Fatalf("errors = %+v, want %+v", res.Errors, want)
	}
	c, err := e.Take(context.Background(), s.ID)
	if err != nil || c.APIKey != foundKey {
		t.Fatalf("take = %+v %v", c, err)
	}
}

func TestConfigFoldersReadsAtMost50(t *testing.T) {
	root := fstest.MapFS{}
	for i := range 60 {
		root[fmt.Sprintf("d%02d/app.conf", i)] = &fstest.MapFile{Data: []byte("k")}
	}
	e, _ := newEngine(t, newStore(t), reader{info("fake", 8989)})
	res, err := e.Local(context.Background(), nil, onDisk(t, root))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suggestions) != 50 || len(res.Errors) != 1 || res.Errors[0].Target != "d50" {
		t.Fatalf("%d suggestions, errors %+v", len(res.Suggestions), res.Errors)
	}
}

func TestDedupeAgainstRegisteredAndDismissed(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	if _, err := st.CreateService(ctx, store.Service{Type: "fake", Name: "Known", BaseURL: "HTTP://Known"},
		func(int64) ([]byte, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	e, _ := newEngine(t, st, fingerprinter{info("fake", 80), okFor("known", "new", "gone")})
	res := scan(t, e, "known", "new", "gone", "new")
	if len(res.Suggestions) != 2 || res.Suggestions[0].URL != "http://gone:80/" || res.Suggestions[1].URL != "http://new:80/" {
		t.Fatalf("suggestions = %+v", res.Suggestions)
	}
	dismissed, err := e.Dismiss(ctx, res.Suggestions[0].ID)
	if err != nil || dismissed.URL != "http://gone:80/" {
		t.Fatalf("dismiss = %+v %v", dismissed, err)
	}
	if _, err := e.Dismiss(ctx, res.Suggestions[0].ID); !errors.Is(err, ErrNoSuggestion) {
		t.Fatalf("second dismiss = %v", err)
	}
	// A fresh engine (a restart) still skips it; a rescan replaces the earlier "new" suggestion.
	e2, _ := newEngine(t, st, fingerprinter{info("fake", 80), okFor("known", "new", "gone")})
	if res := scan(t, e2, "gone", "new"); len(res.Suggestions) != 1 || res.Suggestions[0].URL != "http://new:80/" {
		t.Fatalf("after dismissal = %+v", res.Suggestions)
	}
	scan(t, e2, "new")
	if got := e2.Suggestions(); len(got) != 1 {
		t.Fatalf("rescan kept duplicates: %+v", got)
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://Host":             "http://host:80",
		"HTTP://host:80/":         "http://host:80",
		"https://host/App/":       "https://host:443/App",
		"http://[FD00::1]:8989//": "http://[fd00::1]:8989",
		"http://:8989/":           "http://:8989",
	} {
		u, err := url.Parse(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := normalizeURL(u); got != want {
			t.Errorf("normalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuggestionsExpire(t *testing.T) {
	e, clk := newEngine(t, newStore(t), fingerprinter{info("fake", 80), okFor("h1")})
	id := scan(t, e, "h1").Suggestions[0].ID
	clk.add(59 * time.Minute)
	if _, ok := e.Peek(id); !ok {
		t.Fatal("suggestion expired early")
	}
	clk.add(time.Minute)
	if _, err := e.Take(context.Background(), id); !errors.Is(err, ErrNoSuggestion) || len(e.Suggestions()) != 0 {
		t.Fatal("suggestion outlived its hour")
	}
}

func TestSuggestionsCapped(t *testing.T) {
	e, _ := newEngine(t, newStore(t), fingerprinter{info("fake", 80),
		func(context.Context, adapter.Target) (bool, string, error) { return true, "", nil }})
	hosts := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s%d", prefix, i)
		}
		return out
	}
	first := scan(t, e, hosts("a", 495)...)
	second := scan(t, e, hosts("b", 10)...)
	if len(second.Suggestions) != 10 || len(e.Suggestions()) != maxSuggestions {
		t.Fatalf("%d new, %d stored", len(second.Suggestions), len(e.Suggestions()))
	}
	// The oldest five are gone: entries are kept in the order they were found, newest last.
	gone := 0
	for _, s := range first.Suggestions {
		if _, ok := e.Peek(s.ID); !ok {
			gone++
		}
	}
	if gone != 5 {
		t.Fatalf("%d of the first run's suggestions dropped, want 5", gone)
	}
	if res := scan(t, e, hosts("c", 600)...); len(res.Suggestions) != maxSuggestions {
		t.Fatalf("a run of 600 returned %d suggestions, want the %d kept", len(res.Suggestions), maxSuggestions)
	}
}

func TestTakeOnce(t *testing.T) {
	e, _ := newEngine(t, newStore(t), reader{info("fake", 8989)})
	res, err := e.Local(context.Background(), nil, onDisk(t, fstest.MapFS{"s/app.conf": {Data: []byte(foundKey)}}))
	if err != nil {
		t.Fatal(err)
	}
	id := res.Suggestions[0].ID
	if c, ok := e.Peek(id); !ok || c.APIKey != foundKey {
		t.Fatal("peek lost the key")
	}
	var wg sync.WaitGroup
	var taken atomic.Int32
	for range 10 {
		wg.Go(func() {
			if c, err := e.Take(context.Background(), id); err == nil && c.APIKey == foundKey {
				taken.Add(1)
			}
		})
	}
	wg.Wait()
	if taken.Load() != 1 {
		t.Fatalf("taken %d times, want once", taken.Load())
	}
}

func TestSuggestionJSONNeverHoldsTheKey(t *testing.T) {
	e, _ := newEngine(t, newStore(t), reader{info("fake", 8989)})
	res, err := e.Local(context.Background(), nil, onDisk(t, fstest.MapFS{"s/app.conf": {Data: []byte(foundKey)}}))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []any{res, e.Suggestions()} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), foundKey) || !strings.Contains(string(b), `"keyFound":true`) {
			t.Fatalf("JSON = %s", b)
		}
	}
}

func TestScanNetworkTriesNextAdapterAfterAnError(t *testing.T) {
	// The first adapter gets an answer it cannot use; the second, on the same port, still gets its
	// try. Only "nothing answers" stops the address (TestScanNetworkRefusedPortNotRetried).
	e, _ := newEngine(t, newStore(t),
		fingerprinter{info("alpha", 80), func(context.Context, adapter.Target) (bool, string, error) {
			return false, "", fmt.Errorf("fake: %w", adapter.ErrNotJSON)
		}},
		fingerprinter{info("beta", 80), okFor("h1")})
	res := scan(t, e, "h1")
	if len(res.Suggestions) != 1 || res.Suggestions[0].Type != "beta" {
		t.Fatalf("suggestions = %+v", res.Suggestions)
	}
	if len(res.Errors) != 1 || res.Errors[0] != (SourceError{"h1:80", adapter.ErrNotJSON.Error()}) {
		t.Fatalf("errors = %+v", res.Errors)
	}
}

func TestTakenSuggestionIsNotSuggestedAgain(t *testing.T) {
	st := newStore(t)
	root := fstest.MapFS{"s/app.conf": {Data: []byte(foundKey)}}
	e, _ := newEngine(t, st, reader{info("fake", 8989)})
	res, err := e.Local(context.Background(), nil, onDisk(t, root))
	if err != nil || len(res.Suggestions) != 1 {
		t.Fatalf("config = %+v %v", res, err)
	}
	if _, err := e.Take(context.Background(), res.Suggestions[0].ID); err != nil {
		t.Fatal(err)
	}
	// A host-less find matches no registered URL; the record made by Take keeps it away, also
	// after a restart.
	e2, _ := newEngine(t, st, reader{info("fake", 8989)})
	if res, err := e2.Local(context.Background(), nil, onDisk(t, root)); err != nil || len(res.Suggestions) != 0 {
		t.Fatalf("after take = %+v %v", res, err)
	}
}

func TestTakenFindWithHostCanComeBack(t *testing.T) {
	st := newStore(t)
	e, _ := newEngine(t, st, fingerprinter{info("fake", 80), okFor("h1")})
	if _, err := e.Take(context.Background(), scan(t, e, "h1").Suggestions[0].ID); err != nil {
		t.Fatal(err)
	}
	// Registered services filter it while one exists; nothing is recorded for it, so once that
	// service is gone, it is suggested again.
	if d, err := st.Dismissed(context.Background()); err != nil || len(d) != 0 {
		t.Fatalf("dismissed = %v %v", d, err)
	}
	if res := scan(t, e, "h1"); len(res.Suggestions) != 1 {
		t.Fatalf("rescan = %+v", res)
	}
}

func TestForget(t *testing.T) {
	e, _ := newEngine(t, newStore(t), fingerprinter{info("fake", 80), okFor("h1", "h2")},
		fingerprinter{info("other", 80), okFor()})
	scan(t, e, "h1", "h2")
	ctx := context.Background()
	if err := e.Forget(ctx, "other", &url.URL{Scheme: "http", Host: "h1"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.Forget(ctx, "fake", &url.URL{Scheme: "HTTP", Host: "H1:80", Path: "/"}, ""); err != nil {
		t.Fatal(err)
	}
	if got := e.Suggestions(); len(got) != 1 || got[0].URL != "http://h2:80/" {
		t.Fatalf("after forget = %+v", got)
	}
}

// An app added under an address of its own also stands for the host-less find holding its key.
func TestForgetHostlessFindWithTheSameKey(t *testing.T) {
	st := newStore(t)
	apps := []adapter.Adapter{newLocalApp("sonarr", 8989), newLocalApp("radarr", 8989)}
	e, _ := newEngine(t, st, apps...)
	// The same key in a folder of another type: only a key of the added type counts.
	root := onDisk(t, fstest.MapFS{"sonarr/app.conf": {Data: []byte(foundKey)}, "radarr/app.conf": {Data: []byte(foundKey)}})
	ctx := context.Background()
	if res, err := e.Local(ctx, nil, root); err != nil || len(res.Suggestions) != 2 {
		t.Fatalf("Local = %+v %v", res, err)
	}
	u := &url.URL{Scheme: "http", Host: "tv.lan:8989"}
	if err := e.Forget(ctx, "sonarr", u, "another-key"); err != nil {
		t.Fatal(err)
	}
	if len(e.Suggestions()) != 2 {
		t.Fatalf("a find with another key was forgotten: %+v", e.Suggestions())
	}
	if err := e.Forget(ctx, "sonarr", u, foundKey); err != nil {
		t.Fatal(err)
	}
	if got := e.Suggestions(); len(got) != 1 || got[0].Type != "radarr" {
		t.Fatalf("after forget = %+v", got)
	}
	// Recorded: it stays away, also after a restart.
	e2, _ := newEngine(t, st, apps...)
	if res, err := e2.Local(ctx, nil, root); err != nil || len(res.Suggestions) != 1 || res.Suggestions[0].Type != "radarr" {
		t.Fatalf("after a restart = %+v %v", res, err)
	}
}
