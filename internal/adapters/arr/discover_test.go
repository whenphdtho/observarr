package arr_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"observarr/internal/adapter"
	"observarr/internal/adapters/arr"
)

func newFingerprintTarget(t *testing.T, serverURL string) adapter.Target {
	t.Helper()
	base, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", serverURL, err)
	}
	return adapter.Target{BaseURL: base, HTTP: &http.Client{}}
}

func TestFingerprintOK(t *testing.T) {
	var gotKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Sonarr</title></head><body></body></html>"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	ok, version, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), newFingerprintTarget(t, ts.URL))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if !ok {
		t.Error("ok = false, want true")
	}
	if version != "" {
		t.Errorf("version = %q, want empty (Fingerprint needs a key for the real version)", version)
	}
	if gotKey != "" {
		t.Errorf("Fingerprint sent an API key %q, want none ever", gotKey)
	}
}

func TestFingerprintWrongTitle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Radarr</title></head></html>"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	ok, _, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), newFingerprintTarget(t, ts.URL))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if ok {
		t.Error("ok = true, want false (title names a different app)")
	}
}

func TestFingerprintPingMissing(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Sonarr</title></head></html>"))
	})
	// No /ping handler: ServeMux answers it with its own 404.
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	ok, _, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), newFingerprintTarget(t, ts.URL))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if ok {
		t.Error("ok = true, want false (no /ping support)")
	}
}

func TestFingerprintHTMLOnly(t *testing.T) {
	// A plain web server: every path, including /ping, answers the same HTML page.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><head><title>Sonarr</title></head><body>Not an API</body></html>"))
	}))
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	ok, _, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), newFingerprintTarget(t, ts.URL))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if ok {
		t.Error("ok = true, want false (/ping is not JSON)")
	}
}

func TestFingerprintConnectionRefusedIsError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := ts.URL
	ts.Close() // nothing listens here any more

	a := arr.New(definitionFor(t, "sonarr"))
	_, _, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), newFingerprintTarget(t, addr))
	if err == nil {
		t.Fatal("Fingerprint against a closed port: want an error, got nil")
	}
}

func TestFingerprintAcceptsLoginPageTitleAfterFormsAuthRedirect(t *testing.T) {
	// Forms auth (the default since v4): GET / redirects (same host) to /login, whose own
	// static page is titled "Login - <AppName>", not "<AppName>" alone.
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Login - Sonarr</title></head></html>"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	ok, _, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), newFingerprintTarget(t, ts.URL))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if !ok {
		t.Error("ok = false, want true (same-host redirect to a page titled \"Login - Sonarr\")")
	}
}

func TestFingerprintLoginPageTitleRequiresExactAppName(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>Login - Radarr</title></head></html>"))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	ok, _, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), newFingerprintTarget(t, ts.URL))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if ok {
		t.Error("ok = true, want false (login page names a different app)")
	}
}

func TestFingerprintCrossHostRedirectIsNotRecognised(t *testing.T) {
	// An SSO gateway or reverse proxy in front of the real app might redirect "/" off-host; the
	// shared HTTP client itself refuses to follow that (adapter.NewHTTPClient's checkRedirect),
	// and Fingerprint must treat the refusal as "not recognisable", not a hard error.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><head><title>SSO</title></head></html>"))
	}))
	defer other.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/login", http.StatusFound)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	base, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	target := adapter.Target{BaseURL: base, HTTP: adapter.NewHTTPClient()}

	a := arr.New(definitionFor(t, "sonarr"))
	ok, _, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), target)
	if err != nil {
		t.Fatalf("Fingerprint: %v, want ok=false err=nil (cross-host redirect refused, not a hard failure)", err)
	}
	if ok {
		t.Error("ok = true, want false")
	}
}

func TestFingerprintBasicAuth401IsNotRecognised(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"OK"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Basic realm="restricted"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "sonarr"))
	ok, _, err := a.(adapter.Fingerprinter).Fingerprint(context.Background(), newFingerprintTarget(t, ts.URL))
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	if ok {
		t.Error("ok = true, want false (401 behind HTTP Basic auth)")
	}
}

func TestFingerprintDroppedConnectionAfterSameHostRedirectIsError(t *testing.T) {
	// Only the client's own cross-host refusal means "not recognisable"; a transport failure is
	// an error even when the request URL happens to spell out that refusal's message.
	const phrase = "/refusing a redirect to a different host"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ping" {
			http.Redirect(w, r, phrase, http.StatusFound)
			return
		}
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			conn.Close()
		}
	}))
	defer ts.Close()

	base, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	a := arr.New(definitionFor(t, "sonarr"))
	_, _, err = a.(adapter.Fingerprinter).Fingerprint(context.Background(),
		adapter.Target{BaseURL: base, HTTP: adapter.NewHTTPClient()})
	if err == nil {
		t.Fatal("Fingerprint after a dropped connection: want an error, got nil")
	}

	// The URL above ends up escaped in the error text, so also check a transport failure whose
	// message carries the phrase verbatim: only errors.Is, not the text, may decide.
	failing := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("proxy says: refusing a redirect to a different host")
	})}
	_, _, err = a.(adapter.Fingerprinter).Fingerprint(context.Background(),
		adapter.Target{BaseURL: base, HTTP: failing})
	if err == nil {
		t.Fatal("Fingerprint after a transport failure naming the phrase: want an error, got nil")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestEveryArrAdapterImplementsFingerprinterAndConfigReader(t *testing.T) {
	for _, typ := range []string{"sonarr", "radarr", "lidarr", "prowlarr"} {
		a := arr.New(definitionFor(t, typ))
		if _, ok := a.(adapter.Fingerprinter); !ok {
			t.Errorf("arr.New(%q) does not implement adapter.Fingerprinter", typ)
		}
		if _, ok := a.(adapter.ConfigReader); !ok {
			t.Errorf("arr.New(%q) does not implement adapter.ConfigReader", typ)
		}
	}
}

func TestOnlyProwlarrIsALinker(t *testing.T) {
	for _, typ := range []string{"sonarr", "radarr", "lidarr"} {
		a := arr.New(definitionFor(t, typ))
		if _, ok := a.(adapter.Linker); ok {
			t.Errorf("arr.New(%q) implements adapter.Linker, want only Prowlarr to", typ)
		}
	}
	a := arr.New(definitionFor(t, "prowlarr"))
	if _, ok := a.(adapter.Linker); !ok {
		t.Error("arr.New(\"prowlarr\") does not implement adapter.Linker")
	}
}

func applicationsField(name string, value any) string {
	if s, ok := value.(string); ok {
		return fmt.Sprintf(`{"name":%q,"value":%q}`, name, s)
	}
	return fmt.Sprintf(`{"name":%q,"value":%v}`, name, value)
}

func TestLinkedAppsMapsSonarrAndRadarr(t *testing.T) {
	// A real Prowlarr always masks a set apiKey as "********" (see applicationItem's doc
	// comment) — this is the realistic response shape, and the point of this test is that it
	// must NOT come back as the candidate's key.
	var gotPath, gotKey string
	body := `[
		{"id":1,"name":"My Sonarr","implementation":"Sonarr","fields":[` +
		applicationsField("prowlarrUrl", "http://prowlarr:9696") + "," +
		applicationsField("baseUrl", "http://sonarr.local:8989") + "," +
		applicationsField("apiKey", "********") + `]},
		{"id":2,"name":"My Radarr","implementation":"radarr","fields":[` +
		applicationsField("baseUrl", "http://radarr.local:7878") + "," +
		applicationsField("apiKey", "********") + `]}
	]`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Api-Key")
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "prowlarr"))
	candidates, err := a.(adapter.Linker).LinkedApps(context.Background(), newTarget(t, ts.URL, "prowlarr-key"))
	if err != nil {
		t.Fatalf("LinkedApps: %v", err)
	}
	if gotPath != "/api/v1/applications" {
		t.Errorf("request path = %q, want /api/v1/applications", gotPath)
	}
	if gotKey != "prowlarr-key" {
		t.Errorf("X-Api-Key = %q, want prowlarr-key", gotKey)
	}
	if len(candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(candidates))
	}

	sonarr := candidates[0]
	if sonarr.Type != "sonarr" || sonarr.Name != "My Sonarr" {
		t.Errorf("candidates[0] = %+v, want Type=sonarr Name=\"My Sonarr\"", sonarr)
	}
	if sonarr.BaseURL == nil || sonarr.BaseURL.String() != "http://sonarr.local:8989" {
		t.Errorf("candidates[0].BaseURL = %v, want http://sonarr.local:8989", sonarr.BaseURL)
	}
	if sonarr.APIKey != "" {
		t.Errorf("candidates[0].APIKey = %q, want empty (Prowlarr only ever hands back the mask)", sonarr.APIKey)
	}
	if sonarr.KeySource != "" {
		t.Errorf("candidates[0].KeySource = %q, want empty (no real key was found)", sonarr.KeySource)
	}

	radarr := candidates[1]
	if radarr.Type != "radarr" { // implementation matched case-insensitively ("radarr")
		t.Errorf("candidates[1].Type = %q, want radarr", radarr.Type)
	}
	if radarr.APIKey != "" || radarr.KeySource != "" {
		t.Errorf("candidates[1] APIKey/KeySource = %q/%q, want both empty", radarr.APIKey, radarr.KeySource)
	}
}

func TestLinkedAppsEmptyKeyFieldIsTreatedAsNoKey(t *testing.T) {
	body := `[{"name":"My Sonarr","implementation":"Sonarr","fields":[` +
		applicationsField("baseUrl", "http://sonarr.local:8989") + "," +
		applicationsField("apiKey", "") + `]}]`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "prowlarr"))
	candidates, err := a.(adapter.Linker).LinkedApps(context.Background(), newTarget(t, ts.URL, "key"))
	if err != nil {
		t.Fatalf("LinkedApps: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	if candidates[0].APIKey != "" || candidates[0].KeySource != "" {
		t.Errorf("APIKey/KeySource = %q/%q, want both empty", candidates[0].APIKey, candidates[0].KeySource)
	}
}

func TestLinkedAppsUnmaskedKeyIsStillUsed(t *testing.T) {
	// Defensive coverage: if Prowlarr ever handed back something other than "" or the mask, it
	// must still be usable — the check is an explicit comparison against the mask, not a blanket
	// "LinkedApps never returns a key".
	body := `[{"name":"My Sonarr","implementation":"Sonarr","fields":[` +
		applicationsField("baseUrl", "http://sonarr.local:8989") + "," +
		applicationsField("apiKey", "not-actually-masked") + `]}]`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "prowlarr"))
	candidates, err := a.(adapter.Linker).LinkedApps(context.Background(), newTarget(t, ts.URL, "key"))
	if err != nil {
		t.Fatalf("LinkedApps: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	if candidates[0].APIKey != "not-actually-masked" {
		t.Errorf("APIKey = %q, want not-actually-masked", candidates[0].APIKey)
	}
	if candidates[0].KeySource != "Prowlarr: My Sonarr" {
		t.Errorf("KeySource = %q, want %q", candidates[0].KeySource, "Prowlarr: My Sonarr")
	}
}

func TestLinkedAppsSkipsUnknownImplementation(t *testing.T) {
	body := `[
		{"name":"My Readarr","implementation":"Readarr","fields":[` +
		applicationsField("baseUrl", "http://readarr.local:8787") + `]},
		{"name":"My Sonarr","implementation":"Sonarr","fields":[` +
		applicationsField("baseUrl", "http://sonarr.local:8989") + `]}
	]`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "prowlarr"))
	candidates, err := a.(adapter.Linker).LinkedApps(context.Background(), newTarget(t, ts.URL, "key"))
	if err != nil {
		t.Fatalf("LinkedApps: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1 (Readarr skipped)", len(candidates))
	}
	if candidates[0].Type != "sonarr" {
		t.Errorf("candidates[0].Type = %q, want sonarr", candidates[0].Type)
	}
}

func TestLinkedAppsSkipsUnparsableOrHostlessURL(t *testing.T) {
	body := `[
		{"name":"Bad","implementation":"Sonarr","fields":[` +
		applicationsField("baseUrl", "http://[::not-a-host") + `]},
		{"name":"NoHost","implementation":"Radarr","fields":[` +
		applicationsField("baseUrl", "") + `]},
		{"name":"Good","implementation":"Lidarr","fields":[` +
		applicationsField("baseUrl", "http://lidarr.local:8686") + `]}
	]`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "prowlarr"))
	candidates, err := a.(adapter.Linker).LinkedApps(context.Background(), newTarget(t, ts.URL, "key"))
	if err != nil {
		t.Fatalf("LinkedApps: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Type != "lidarr" {
		t.Fatalf("candidates = %+v, want only the Lidarr entry", candidates)
	}
}

func TestLinkedAppsRequiresHTTPOrHTTPSScheme(t *testing.T) {
	body := `[
		{"name":"FTP","implementation":"Sonarr","fields":[` +
		applicationsField("baseUrl", "ftp://sonarr.local:8989") + `]},
		{"name":"JS","implementation":"Radarr","fields":[` +
		applicationsField("baseUrl", "javascript:alert(1)") + `]},
		{"name":"Good","implementation":"Lidarr","fields":[` +
		applicationsField("baseUrl", "https://lidarr.local:8686") + `]}
	]`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "prowlarr"))
	candidates, err := a.(adapter.Linker).LinkedApps(context.Background(), newTarget(t, ts.URL, "key"))
	if err != nil {
		t.Fatalf("LinkedApps: %v", err)
	}
	if len(candidates) != 1 || candidates[0].Type != "lidarr" {
		t.Fatalf("candidates = %+v, want only the https Lidarr entry", candidates)
	}
}

func TestLinkedAppsStripsUserinfoQueryAndFragmentFromURL(t *testing.T) {
	body := `[{"name":"My Sonarr","implementation":"Sonarr","fields":[` +
		applicationsField("baseUrl", "http://user:pass@sonarr.local:8989/sonarr?foo=bar#frag") + `]}]`
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "prowlarr"))
	candidates, err := a.(adapter.Linker).LinkedApps(context.Background(), newTarget(t, ts.URL, "key"))
	if err != nil {
		t.Fatalf("LinkedApps: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	u := candidates[0].BaseURL
	if u == nil {
		t.Fatal("BaseURL is nil")
	}
	if u.User != nil {
		t.Errorf("User = %v, want nil (stripped)", u.User)
	}
	if u.RawQuery != "" {
		t.Errorf("RawQuery = %q, want empty (stripped)", u.RawQuery)
	}
	if u.Fragment != "" {
		t.Errorf("Fragment = %q, want empty (stripped)", u.Fragment)
	}
	want := "http://sonarr.local:8989/sonarr"
	if u.String() != want {
		t.Errorf("BaseURL = %q, want %q", u.String(), want)
	}
}

func TestLinkedAppsCapsAt100(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 150; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"name":"Sonarr %d","implementation":"Sonarr","fields":[%s]}`,
			i, applicationsField("baseUrl", fmt.Sprintf("http://sonarr%d.local:8989", i)))
	}
	sb.WriteString("]")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/applications", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sb.String()))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	a := arr.New(definitionFor(t, "prowlarr"))
	candidates, err := a.(adapter.Linker).LinkedApps(context.Background(), newTarget(t, ts.URL, "key"))
	if err != nil {
		t.Fatalf("LinkedApps: %v", err)
	}
	if len(candidates) != 100 {
		t.Fatalf("len(candidates) = %d, want 100 (capped)", len(candidates))
	}
}

func sonarrConfigXML(instanceName string) string {
	return `<Config>
  <Port>8989</Port>
  <SslPort>9898</SslPort>
  <EnableSsl>False</EnableSsl>
  <ApiKey>found-key-abc</ApiKey>
  <UrlBase></UrlBase>` +
		func() string {
			if instanceName == "" {
				return ""
			}
			return "\n  <InstanceName>" + instanceName + "</InstanceName>"
		}() + `
</Config>`
}

func TestReadConfigWithInstanceName(t *testing.T) {
	fsys := fstest.MapFS{
		"config.xml": {Data: []byte(sonarrConfigXML("Sonarr"))},
	}
	a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
	cand, ok, err := a.ReadConfig("", fsys)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if cand.Type != "sonarr" {
		t.Errorf("Type = %q, want sonarr", cand.Type)
	}
	if cand.Name != "Sonarr" {
		t.Errorf("Name = %q, want Sonarr (the InstanceName)", cand.Name)
	}
	if cand.APIKey != "found-key-abc" {
		t.Errorf("APIKey = %q, want found-key-abc", cand.APIKey)
	}
	if cand.BaseURL == nil || cand.BaseURL.Hostname() != "" {
		t.Errorf("BaseURL host = %q, want empty", cand.BaseURL.Hostname())
	}
	if cand.BaseURL.Port() != "8989" {
		t.Errorf("BaseURL port = %q, want 8989", cand.BaseURL.Port())
	}
	if cand.BaseURL.Scheme != "http" {
		t.Errorf("BaseURL scheme = %q, want http", cand.BaseURL.Scheme)
	}
	if cand.KeySource != "config.xml" {
		t.Errorf("KeySource = %q, want config.xml (no dir name given)", cand.KeySource)
	}
}

func TestReadConfigInstanceNameSubstringMatch(t *testing.T) {
	for _, instanceName := range []string{"Sonarr 4K", "Anime Sonarr"} {
		fsys := fstest.MapFS{"config.xml": {Data: []byte(sonarrConfigXML(instanceName))}}
		a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
		cand, ok, err := a.ReadConfig("", fsys)
		if err != nil {
			t.Fatalf("ReadConfig(%q): %v", instanceName, err)
		}
		if !ok {
			t.Fatalf("ReadConfig(%q): ok = false, want true (contains \"Sonarr\")", instanceName)
		}
		if cand.Name != instanceName {
			t.Errorf("ReadConfig(%q): Name = %q, want the InstanceName verbatim", instanceName, cand.Name)
		}
	}
}

func TestReadConfigDirNameFallback(t *testing.T) {
	fsys := fstest.MapFS{"config.xml": {Data: []byte(sonarrConfigXML(""))}}
	a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
	cand, ok, err := a.ReadConfig("sonarr-appdata", fsys)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true (dir name contains \"sonarr\")")
	}
	if cand.KeySource != "config.xml in sonarr-appdata" {
		t.Errorf("KeySource = %q, want %q", cand.KeySource, "config.xml in sonarr-appdata")
	}

	// A dir name that does NOT mention the app, and no InstanceName: not recognised.
	_, ok, err = a.ReadConfig("media-app-data", fsys)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if ok {
		t.Error("ok = true, want false (dir name doesn't mention sonarr, no InstanceName)")
	}
}

func TestReadConfigAnotherAppsConfigIsNotMine(t *testing.T) {
	fsys := fstest.MapFS{"config.xml": {Data: []byte(sonarrConfigXML("Radarr"))}}
	a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
	// Even a matching dir name must not override an explicit, different InstanceName.
	_, ok, err := a.ReadConfig("sonarr", fsys)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if ok {
		t.Error("ok = true, want false (InstanceName names a different app)")
	}
}

func TestReadConfigNoConfigXML(t *testing.T) {
	fsys := fstest.MapFS{"other.txt": {Data: []byte("hello")}}
	a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
	_, ok, err := a.ReadConfig("sonarr", fsys)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if ok {
		t.Error("ok = true, want false (no config.xml)")
	}
}

func TestReadConfigOversizedFile(t *testing.T) {
	big := make([]byte, (1<<20)+1)
	for i := range big {
		big[i] = 'x'
	}
	fsys := fstest.MapFS{"config.xml": {Data: big}}
	a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
	_, _, err := a.ReadConfig("sonarr", fsys)
	if err == nil || err.Error() != "config.xml exceeds 1048576 bytes" {
		t.Fatalf("ReadConfig with a >1 MiB config.xml: err = %v, want one naming config.xml", err)
	}
}

func TestReadConfigMalformedXMLErrorHasNoFileContent(t *testing.T) {
	const secret = "THIS-SECRET-MUST-NOT-LEAK"
	fsys := fstest.MapFS{"config.xml": {Data: []byte("<Config><ApiKey>" + secret + "</ApiKey" /* unterminated */)}}
	a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
	_, ok, err := a.ReadConfig("sonarr", fsys)
	if ok {
		t.Error("ok = true, want false for malformed XML")
	}
	if err == nil {
		t.Fatal("ReadConfig with malformed XML: want an error, got nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error message contains the file's content: %v", err)
	}
}

func TestReadConfigEnableSslUsesSslPortAndHttps(t *testing.T) {
	xmlBody := `<Config>
  <Port>8989</Port>
  <SslPort>9898</SslPort>
  <EnableSsl>True</EnableSsl>
  <ApiKey>k</ApiKey>
  <UrlBase>/sonarr</UrlBase>
  <InstanceName>Sonarr</InstanceName>
</Config>`
	fsys := fstest.MapFS{"config.xml": {Data: []byte(xmlBody)}}
	a := arr.New(definitionFor(t, "sonarr")).(adapter.ConfigReader)
	cand, ok, err := a.ReadConfig("sonarr", fsys)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if cand.BaseURL.Scheme != "https" {
		t.Errorf("Scheme = %q, want https", cand.BaseURL.Scheme)
	}
	if cand.BaseURL.Port() != "9898" {
		t.Errorf("Port = %q, want 9898 (SslPort)", cand.BaseURL.Port())
	}
	if cand.BaseURL.Path != "/sonarr" {
		t.Errorf("Path = %q, want /sonarr", cand.BaseURL.Path)
	}
}

func TestMatchesImage(t *testing.T) {
	sonarr, prowlarr := arr.New(arr.Definitions[0]).(adapter.ImageMatcher), arr.New(arr.Definitions[3]).(adapter.ImageMatcher)
	for _, image := range []string{
		"lscr.io/linuxserver/sonarr:latest", "linuxserver/sonarr", "docker.io/linuxserver/sonarr:4.0.10",
		"ghcr.io/hotio/sonarr:release", "hotio/sonarr@sha256:0123abcd", "LSCR.IO:443/LinuxServer/Sonarr:develop",
		"ghcr.io/linuxserver/sonarr", "sonarr", "sonarr:local", "docker.io/library/sonarr",
	} {
		if !sonarr.MatchesImage(image) {
			t.Errorf("%q is not recognised as Sonarr", image)
		}
		if prowlarr.MatchesImage(image) {
			t.Errorf("%q is recognised as Prowlarr", image)
		}
	}
	for _, image := range []string{
		"lscr.io/linuxserver/radarr", "someone/sonarr", "example.com/linuxserver/sonarr", "sonarr-exporter",
		"linuxserver/sonarr-extra:latest", "sha256:abcdef", "", "lscr.io/linuxserver/sonarr/extra",
	} {
		if sonarr.MatchesImage(image) {
			t.Errorf("%q is recognised as Sonarr", image)
		}
	}
	if !prowlarr.MatchesImage("lscr.io/linuxserver/prowlarr:latest") {
		t.Error("the linking adapter does not match its image")
	}
}
