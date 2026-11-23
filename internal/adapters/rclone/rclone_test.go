package rclone_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"observarr/internal/adapter"
	"observarr/internal/adapters/rclone"
	"observarr/internal/shape"
)

// readOnlyPaths is every call the adapter may make. Anything else reaching the fake server fails the test.
var readOnlyPaths = []string{"/core/version", "/rc/noopauth", "/core/stats", "/mount/listmounts", "/vfs/list",
	"/vfs/stats"}

// authPaths need authentication on a real remote control, as rclone registers them without NoAuth.
var authPaths = []string{"/rc/noopauth", "/mount/listmounts"}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// fakeRC imitates rclone's remote control: POST only, JSON parameters, basic auth when user is set, and, without
// a user and without noAuth, a 403 for the calls that need authentication.
type fakeRC struct {
	t          *testing.T
	user, pass string
	noAuth     bool
	answers    map[string][]byte // by path; vfs/stats by "/vfs/stats?" + fs
	status     map[string]int    // by path

	mu    sync.Mutex
	calls []string
}

func newFakeRC(t *testing.T) *fakeRC {
	f := &fakeRC{t: t, noAuth: true, status: map[string]int{}, answers: map[string][]byte{
		"/core/version":                 fixture(t, "core_version.json"),
		"/rc/noopauth":                  []byte(`{}`),
		"/core/stats":                   fixture(t, "core_stats.json"),
		"/mount/listmounts":             fixture(t, "listmounts.json"),
		"/vfs/list":                     fixture(t, "vfs_list.json"),
		"/vfs/stats?media-crypt:":       fixture(t, "vfs_stats_media_crypt.json"),
		"/vfs/stats?cloud:backup":       fixture(t, "vfs_stats_cloud_backup.json"),
		"/mount/listmounts?notfound404": fixture(t, "error_not_found.json"),
	}}
	return f
}

func (f *fakeRC) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	if !slices.Contains(readOnlyPaths, r.URL.Path) {
		f.t.Errorf("call outside the read-only list: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		f.t.Errorf("%s: Content-Type %q", r.URL.Path, ct)
	}
	if f.user != "" {
		u, p, ok := r.BasicAuth()
		if !ok || u != f.user || p != f.pass {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	} else if !f.noAuth && slices.Contains(authPaths, r.URL.Path) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"authentication must be set up on the rc server","status":403}`))
		return
	}
	var params map[string]string
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		f.t.Errorf("%s: parameters are not a JSON object: %v", r.URL.Path, err)
	}
	key := r.URL.Path
	if key == "/vfs/stats" {
		key += "?" + params["fs"]
	}
	if code := f.status[r.URL.Path]; code != 0 {
		w.WriteHeader(code)
		w.Write(f.answers["/mount/listmounts?notfound404"])
		return
	}
	body, ok := f.answers[key]
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"no VFS found","status":500}`))
		return
	}
	w.Write(body)
}

func (f *fakeRC) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func start(t *testing.T, f *fakeRC) *httptest.Server {
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	return ts
}

func target(t *testing.T, serverURL, key string) adapter.Target {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	return adapter.Target{BaseURL: u, APIKey: key, HTTP: adapter.NewHTTPClient()}
}

func TestInfo(t *testing.T) {
	info := rclone.New().Info()
	if info.Type != "rclone" || info.NeedsAPIKey || info.KeyLabel == "" || info.DefaultPort != 5572 {
		t.Errorf("Info = %+v", info)
	}
	if !slices.Equal(info.Kinds, []shape.Kind{shape.KindHealth, shape.KindMounts}) {
		t.Errorf("Kinds = %v", info.Kinds)
	}
}

func TestDetectWithoutCredentials(t *testing.T) {
	f := newFakeRC(t)
	ts := start(t, f)
	id, err := rclone.New().Detect(context.Background(), target(t, ts.URL, ""))
	if err != nil {
		t.Fatal(err)
	}
	if id.Version != "1.71.2" {
		t.Errorf("Version = %q, want 1.71.2", id.Version)
	}
	if got := f.paths(); !slices.Equal(got, []string{"POST /core/version"}) {
		t.Errorf("calls = %v", got)
	}
}

func TestDetectWithCredentials(t *testing.T) {
	f := newFakeRC(t)
	f.user, f.pass = "observer", "s3cret:with-colon"
	ts := start(t, f)
	id, err := rclone.New().Detect(context.Background(), target(t, ts.URL, "observer:s3cret:with-colon"))
	if err != nil {
		t.Fatal(err)
	}
	if id.Version != "1.71.2" {
		t.Errorf("Version = %q", id.Version)
	}
	if got := f.paths(); !slices.Equal(got, []string{"POST /core/version", "POST /rc/noopauth"}) {
		t.Errorf("calls = %v", got)
	}
}

func TestDetectWrongCredentials(t *testing.T) {
	f := newFakeRC(t)
	f.user, f.pass = "observer", "right"
	ts := start(t, f)
	_, err := rclone.New().Detect(context.Background(), target(t, ts.URL, "observer:wrong-password"))
	if !errors.Is(err, adapter.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "wrong-password") {
		t.Errorf("error %q holds the password", err)
	}
}

// A remote control without --rc-user and without --rc-no-auth refuses rc/noopauth: credentials the user entered
// then cannot be checked, which Detect reports.
func TestDetectCredentialsAgainstServerWithoutAuth(t *testing.T) {
	f := newFakeRC(t)
	f.noAuth = false
	ts := start(t, f)
	_, err := rclone.New().Detect(context.Background(), target(t, ts.URL, "observer:pw"))
	if !errors.Is(err, adapter.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestDetectMalformedCredentialsSendsNothing(t *testing.T) {
	f := newFakeRC(t)
	ts := start(t, f)
	for _, key := range []string{"no-colon", ":password-only"} {
		_, err := rclone.New().Detect(context.Background(), target(t, ts.URL, key))
		if !errors.Is(err, rclone.ErrCredentials) {
			t.Errorf("%q: err = %v, want ErrCredentials", key, err)
		}
		if strings.Contains(err.Error(), key) {
			t.Errorf("error %q holds the key", err)
		}
	}
	if got := f.paths(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
}

func TestDetectOtherApps(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"html page": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<!doctype html><title>Login</title>"))
		},
		"not found":          func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) },
		"method not allowed": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) },
		"other json":         func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"version":"4.0.9"}`)) },
		"null":               func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`null`)) },
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(h)
			defer ts.Close()
			_, err := rclone.New().Detect(context.Background(), target(t, ts.URL, ""))
			if !errors.Is(err, adapter.ErrWrongApp) {
				t.Errorf("err = %v, want ErrWrongApp", err)
			}
		})
	}
}

func probeMounts(t *testing.T, f *fakeRC, key string) shape.Mounts {
	t.Helper()
	ts := start(t, f)
	got, err := rclone.New().Probe(context.Background(), target(t, ts.URL, key), shape.KindMounts)
	if err != nil {
		t.Fatal(err)
	}
	return got.(shape.Mounts)
}

func TestProbeMounts(t *testing.T) {
	m := probeMounts(t, newFakeRC(t), "")
	if m.Total != 2 || len(m.Mounts) != 2 {
		t.Fatalf("Total %d, %d mounts, want 2 and 2", m.Total, len(m.Mounts))
	}
	// Sorted by mount point: the one rclone does not report a mount point for comes first.
	backup, media := m.Mounts[0], m.Mounts[1]
	wantMedia := shape.Mount{Remote: "media-crypt:", MountPoint: "/mnt/media", State: shape.MountMounted,
		CacheMode: "full", Cache: true, CacheUsedBytes: 64424509440, CacheMaxBytes: 107374182400, CachedFiles: 318,
		UploadsInProgress: 2, UploadsQueued: 5, ErroredFiles: 1}
	if media != wantMedia {
		t.Errorf("media mount\n got %+v\nwant %+v", media, wantMedia)
	}
	wantBackup := shape.Mount{Remote: "cloud:backup", State: shape.MountActive, CacheMode: "off"}
	if backup != wantBackup {
		t.Errorf("backup mount\n got %+v\nwant %+v", backup, wantBackup)
	}
	if m.Speed != 18350080.5 || m.Bytes != 52613349376 || m.Errors != 2 || m.ActiveTransfers != 2 {
		t.Errorf("stats = speed %v bytes %d errors %d active %d", m.Speed, m.Bytes, m.Errors, m.ActiveTransfers)
	}
	want := []shape.Transfer{
		{Name: "Films/A Quiet Field (2024)/A Quiet Field (2024).mkv", Progress: 0.62, SizeBytes: 1183842304, Speed: 12582912},
		{Name: "Shows/Northern Lights/Season 01/Northern Lights - S01E03.mkv", Progress: 0.12, SizeBytes: 873463808, Speed: 5767168},
	}
	if !slices.Equal(m.Transfers, want) {
		t.Errorf("transfers\n got %+v\nwant %+v", m.Transfers, want)
	}
}

func TestProbeMountsIdleMarshalsEmptyLists(t *testing.T) {
	f := newFakeRC(t)
	f.answers["/core/stats"] = fixture(t, "core_stats_idle.json")
	f.answers["/vfs/list"] = []byte(`{"vfses":[]}`)
	f.answers["/mount/listmounts"] = []byte(`{"mountPoints":[]}`)
	m := probeMounts(t, f, "")
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"mounts":[]`) || !strings.Contains(string(b), `"transfers":[]`) {
		t.Errorf("marshalled %s, want empty lists, not null", b)
	}
}

// Without credentials and without --rc-no-auth, mount/listmounts is refused; the mounts still show, without
// their mount points.
func TestProbeMountsWithoutAuthHasNoMountPoints(t *testing.T) {
	f := newFakeRC(t)
	f.noAuth = false
	m := probeMounts(t, f, "")
	for _, mt := range m.Mounts {
		if mt.State != shape.MountActive || mt.MountPoint != "" {
			t.Errorf("mount %+v, want active without a mount point", mt)
		}
	}
	if len(m.Mounts) != 2 {
		t.Errorf("%d mounts, want 2", len(m.Mounts))
	}
}

func TestProbeMountsWithoutMountSupport(t *testing.T) {
	f := newFakeRC(t)
	f.status["/mount/listmounts"] = http.StatusNotFound
	m := probeMounts(t, f, "")
	if len(m.Mounts) != 2 || m.Mounts[1].State != shape.MountActive {
		t.Errorf("mounts %+v", m.Mounts)
	}
}

func TestProbeMountsWrongCredentialsFails(t *testing.T) {
	f := newFakeRC(t)
	f.user, f.pass = "observer", "right"
	ts := start(t, f)
	_, err := rclone.New().Probe(context.Background(), target(t, ts.URL, "observer:wrong"), shape.KindMounts)
	if !errors.Is(err, adapter.ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestProbeMountsSkipsVFSThatWentAway(t *testing.T) {
	f := newFakeRC(t)
	delete(f.answers, "/vfs/stats?cloud:backup")
	m := probeMounts(t, f, "")
	if len(m.Mounts) != 1 || m.Mounts[0].Remote != "media-crypt:" {
		t.Errorf("mounts %+v", m.Mounts)
	}
}

func TestProbeMountsCapsMountsAndTransfers(t *testing.T) {
	f := newFakeRC(t)
	names := make([]string, 100)
	for i := range names {
		names[i] = fmt.Sprintf("remote%03d:", i)
		f.answers["/vfs/stats?"+names[i]] = []byte(fmt.Sprintf(`{"fs":%q,"opt":{"CacheMode":"writes"}}`, names[i]))
	}
	list, _ := json.Marshal(map[string][]string{"vfses": names})
	f.answers["/vfs/list"] = list
	var tr strings.Builder
	tr.WriteString(`{"speed":1,"transferring":[`)
	for i := range 500 {
		if i > 0 {
			tr.WriteString(",")
		}
		fmt.Fprintf(&tr, `{"name":"file-%d","percentage":50,"size":10,"speed":1}`, i)
	}
	tr.WriteString(`],"errors":3}`)
	f.answers["/core/stats"] = []byte(tr.String())

	m := probeMounts(t, f, "")
	if m.Total != 100 || len(m.Mounts) != shape.MaxMounts {
		t.Errorf("Total %d with %d mounts, want 100 with %d", m.Total, len(m.Mounts), shape.MaxMounts)
	}
	if m.ActiveTransfers != 500 || len(m.Transfers) != shape.MaxTransfers || m.Errors != 3 {
		t.Errorf("%d active, %d kept, %d errors", m.ActiveTransfers, len(m.Transfers), m.Errors)
	}
	if m.Mounts[0].CacheMode != "writes" {
		t.Errorf("cache mode %q", m.Mounts[0].CacheMode)
	}
	stats := 0
	for _, c := range f.paths() {
		if c == "POST /vfs/stats" {
			stats++
		}
	}
	if stats != shape.MaxMounts {
		t.Errorf("%d vfs/stats calls, want %d", stats, shape.MaxMounts)
	}
}

func TestProbeHealthWarnsOfCacheProblems(t *testing.T) {
	f := newFakeRC(t)
	f.answers["/vfs/stats?cloud:backup"] = []byte(`{"fs":"cloud:backup","diskCache":{"outOfSpace":true},"opt":{"CacheMode":2}}`)
	ts := start(t, f)
	got, err := rclone.New().Probe(context.Background(), target(t, ts.URL, ""), shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	h := got.(shape.Health)
	if h.Version != "1.71.2" {
		t.Errorf("Version = %q", h.Version)
	}
	want := []shape.Warning{
		{Level: shape.LevelError, Source: "cloud:backup", Message: "the cache disk is out of space"},
		{Level: shape.LevelWarning, Source: "media-crypt:", Message: "1 cached file could not be uploaded"},
	}
	if !slices.Equal(h.Warnings, want) {
		t.Errorf("warnings\n got %+v\nwant %+v", h.Warnings, want)
	}
}

func TestProbeUnknownKind(t *testing.T) {
	_, err := rclone.New().Probe(context.Background(), adapter.Target{}, shape.KindQueue)
	if !errors.Is(err, adapter.ErrUnknownKind) {
		t.Errorf("err = %v", err)
	}
}

// TestOnlyReadOnlyCalls runs every Detect and Probe path and checks that each request was a POST to one of the
// read-only calls; the fake server also fails the test on any other path.
func TestOnlyReadOnlyCalls(t *testing.T) {
	f := newFakeRC(t)
	f.user, f.pass = "observer", "pw"
	ts := start(t, f)
	tg := target(t, ts.URL, "observer:pw")
	a := rclone.New()
	if _, err := a.Detect(context.Background(), tg); err != nil {
		t.Fatal(err)
	}
	for _, k := range a.Info().Kinds {
		if _, err := a.Probe(context.Background(), tg, k); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for _, c := range f.paths() {
		method, path, _ := strings.Cut(c, " ")
		if method != http.MethodPost || !slices.Contains(readOnlyPaths, path) {
			t.Errorf("unexpected call %s", c)
		}
		seen[path] = true
	}
	for _, p := range readOnlyPaths {
		if !seen[p] {
			t.Errorf("%s was never called; the list above is out of date", p)
		}
	}
}
