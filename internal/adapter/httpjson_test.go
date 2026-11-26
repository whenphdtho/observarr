package adapter

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestGetJSONSendsHeaderAndJoinsPathPrefix(t *testing.T) {
	var gotPath, gotKey, gotAccept string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("X-Api-Key")
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"4.1.0"}`))
	}))
	defer ts.Close()

	base, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	base.Path = "/sonarr"
	tgt := Target{BaseURL: base, APIKey: "the-key", HTTP: ts.Client()}

	var out struct {
		Version string `json:"version"`
	}
	if err := GetJSON(context.Background(), tgt, "/api/v3/system/status", "X-Api-Key", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if gotPath != "/sonarr/api/v3/system/status" {
		t.Errorf("path = %q, want /sonarr/api/v3/system/status", gotPath)
	}
	if gotKey != "the-key" {
		t.Errorf("X-Api-Key header = %q, want the-key", gotKey)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept header = %q, want application/json", gotAccept)
	}
	if out.Version != "4.1.0" {
		t.Errorf("Version = %q, want 4.1.0", out.Version)
	}
}

func TestGetJSONNoKeyHeaderWhenAPIKeyEmpty(t *testing.T) {
	var sawHeader bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header["X-Api-Key"]
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}
	var out map[string]any
	if err := GetJSON(context.Background(), tgt, "/x", "X-Api-Key", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if sawHeader {
		t.Error("X-Api-Key header present, want absent when APIKey is empty")
	}
}

func TestGetJSONUnauthorized(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte("nope"))
		}))
		base, _ := url.Parse(ts.URL)
		tgt := Target{BaseURL: base, APIKey: "topsecret", HTTP: ts.Client()}
		var out map[string]any
		err := GetJSON(context.Background(), tgt, "/x", "X-Api-Key", &out)
		if !errors.Is(err, ErrUnauthorized) {
			t.Errorf("status %d: got err %v, want ErrUnauthorized", code, err)
		}
		ts.Close()
	}
}

func TestGetJSONServerErrorMessageExcludesKey(t *testing.T) {
	const apiKey = "super-secret-key-xyz"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom: " + apiKey))
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, APIKey: apiKey, HTTP: ts.Client()}
	var out map[string]any
	err := GetJSON(context.Background(), tgt, "/x", "X-Api-Key", &out)
	if err == nil {
		t.Fatal("GetJSON with 500: want error, got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error %q: want it to contain \"500\"", err.Error())
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Errorf("error %q: leaked the API key", err.Error())
	}
}

func TestGetJSONBodyTooLarge(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 5<<20) // 5 MiB
		_, _ = w.Write(buf)
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}
	var out map[string]any
	if err := GetJSON(context.Background(), tgt, "/big", "", &out); err == nil {
		t.Fatal("GetJSON with 5 MiB body: want error, got nil")
	}
}

// TestBodyLimitAppliesAfterDecompression sends 16 MiB of JSON that compresses to well under the 4 MiB limit: the
// limit counts what the app's answer unpacks to, not what crossed the network.
func TestBodyLimitAppliesAfterDecompression(t *testing.T) {
	var packed bytes.Buffer
	zw := gzip.NewWriter(&packed)
	zw.Write([]byte("["))
	zw.Write(bytes.Repeat([]byte(" "), 16<<20))
	zw.Write([]byte("]"))
	zw.Close()
	if packed.Len() >= maxBodyBytes {
		t.Fatalf("packed answer is %d bytes", packed.Len())
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Error("the client does not ask for gzip")
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(packed.Bytes())
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	var out []any
	err := GetJSON(context.Background(), Target{BaseURL: base, HTTP: NewHTTPClient()}, "/", "", &out)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("GetJSON = %v, want ErrTooLarge", err)
	}
}

func TestGetJSONRequiresJSONBody(t *testing.T) {
	const bodyMarker = "leaked-body-marker-98765"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>" + bodyMarker + "</html>"))
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}
	var out map[string]any
	err := GetJSON(context.Background(), tgt, "/x", "", &out)
	if !errors.Is(err, ErrNotJSON) {
		t.Fatalf("GetJSON with non-JSON body: got err %v, want ErrNotJSON", err)
	}
	if strings.Contains(err.Error(), bodyMarker) {
		t.Errorf("error %q leaked the response body", err.Error())
	}
}

func TestGetJSON404WrapsErrNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("nothing here"))
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}
	var out map[string]any
	if err := GetJSON(context.Background(), tgt, "/missing", "", &out); !errors.Is(err, ErrNotFound) {
		t.Errorf("got err %v, want ErrNotFound", err)
	}
}

// TestGetJSONDoesNotRequireJSONContentType matters because a simple static file server (e.g.
// `python3 -m http.server`) does not always label .json as application/json.
func TestGetJSONDoesNotRequireJSONContentType(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte(`{"version":"1.2.3"}`))
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}
	var out struct {
		Version string `json:"version"`
	}
	if err := GetJSON(context.Background(), tgt, "/x", "", &out); err != nil {
		t.Fatalf("GetJSON with non-JSON content-type but JSON body: %v", err)
	}
	if out.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", out.Version)
	}
}

func TestGetJSONContextDeadlineHonoured(t *testing.T) {
	block := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	t.Cleanup(func() {
		close(block)
		ts.Close()
	})

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var out map[string]any
	if err := GetJSON(ctx, tgt, "/slow", "", &out); err == nil {
		t.Fatal("GetJSON past context deadline: want error, got nil")
	}
}

func TestGetJSONPreservesEscapedPathCharacters(t *testing.T) {
	var gotRequestURI string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestURI = r.RequestURI
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	base, err := url.Parse(ts.URL + "/my%2Fapp")
	if err != nil {
		t.Fatalf("url.Parse base: %v", err)
	}
	tgt := Target{BaseURL: base, HTTP: ts.Client()}

	var out map[string]any
	if err := GetJSON(context.Background(), tgt, "/api/a%2Fb", "", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	const want = "/my%2Fapp/api/a%2Fb"
	if gotRequestURI != want {
		t.Errorf("request URI = %q, want %q", gotRequestURI, want)
	}
}

func TestGetJSONPropagatesPathParseError(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: ts.Client()}
	var out map[string]any
	// "%zz" is not a valid percent-escape, so url.Parse must fail on it.
	if err := GetJSON(context.Background(), tgt, "/api/%zz", "", &out); err == nil {
		t.Fatal("GetJSON with unparsable path: want error, got nil")
	}
	if called {
		t.Error("handler was invoked despite an unparsable request path")
	}
}

func TestGetJSONForwardsKeyHeaderOnSameHostRedirect(t *testing.T) {
	var secondReqKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "the-key" {
			http.Error(w, "missing key on first request", http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, "/new", http.StatusFound)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		secondReqKey = r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, APIKey: "the-key", HTTP: NewHTTPClient()}

	var out struct {
		Version string `json:"version"`
	}
	if err := GetJSON(context.Background(), tgt, "/old", "X-Api-Key", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if secondReqKey != "the-key" {
		t.Errorf("X-Api-Key on redirected request = %q, want the-key", secondReqKey)
	}
}

func TestGetJSONFollowsSameHostRedirect(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/new", http.StatusFound)
	})
	mux.HandleFunc("/new", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: NewHTTPClient()}

	var out struct {
		Version string `json:"version"`
	}
	if err := GetJSON(context.Background(), tgt, "/old", "", &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if out.Version != "1.0.0" {
		t.Errorf("Version = %q, want 1.0.0", out.Version)
	}
}

func TestGetJSONRefusesCrossHostRedirect(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	}))
	defer other.Close()

	main := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/elsewhere", http.StatusFound)
	}))
	defer main.Close()

	base, _ := url.Parse(main.URL)
	tgt := Target{BaseURL: base, HTTP: NewHTTPClient()}

	var out map[string]any
	if err := GetJSON(context.Background(), tgt, "/old", "", &out); !errors.Is(err, ErrCrossHostRedirect) {
		t.Fatalf("GetJSON with cross-host redirect: err = %v, want ErrCrossHostRedirect", err)
	}
}

// TestGetJSONRefusesRedirectToDifferentHostnameSamePort checks the security property that
// matters: httptest.NewServer listens on 127.0.0.1, and a redirect to "localhost" on the very
// same port must still be refused as a different host.
func TestGetJSONRefusesRedirectToDifferentHostnameSamePort(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/old" {
			_, port, err := net.SplitHostPort(r.Host)
			if err != nil {
				t.Errorf("SplitHostPort(%q): %v", r.Host, err)
				return
			}
			http.Redirect(w, r, "http://localhost:"+port+"/new", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"version":"1.0.0"}`))
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	tgt := Target{BaseURL: base, HTTP: NewHTTPClient()}

	var out map[string]any
	if err := GetJSON(context.Background(), tgt, "/old", "", &out); err == nil {
		t.Fatal("GetJSON with 127.0.0.1 -> localhost redirect: want error, got nil")
	}
}

func TestNewHTTPClientFieldSettings(t *testing.T) {
	c := NewHTTPClient()
	if c.Timeout != 15*time.Second {
		t.Errorf("Timeout = %v, want 15s", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}
	if tr.MaxConnsPerHost != 0 || tr.MaxIdleConnsPerHost != 4 {
		t.Errorf("MaxConnsPerHost = %d, MaxIdleConnsPerHost = %d; want 0 (no cap) and 4",
			tr.MaxConnsPerHost, tr.MaxIdleConnsPerHost)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 10*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 10s", tr.ResponseHeaderTimeout)
	}
	if tr.Proxy != nil {
		t.Error("Transport.Proxy is set, want nil (no proxy from the environment)")
	}
	if c.CheckRedirect == nil {
		t.Error("CheckRedirect is nil, want a cross-host guard")
	}
}

// Eight services behind one host:port, five of which hang: the other three must still be answered at once.
func TestHangingServicesDoNotHoldUpOthersOnTheSameHost(t *testing.T) {
	hanging := make(chan struct{}, 5)
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			hanging <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	defer close(release)

	c := NewHTTPClient()
	base, _ := url.Parse(ts.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 5 {
		go func() {
			var v any
			_ = GetJSON(ctx, Target{BaseURL: base, HTTP: c}, "/hang", "", &v)
		}()
	}
	for i := range 5 {
		select {
		case <-hanging:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d hanging requests reached the server", i)
		}
	}

	start := time.Now()
	errs := make(chan error, 3)
	for range 3 {
		go func() {
			var v any
			errs <- GetJSON(ctx, Target{BaseURL: base, HTTP: c}, "/ok", "", &v)
		}()
	}
	for range 3 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("healthy services took %v behind hanging ones", d)
	}
}

func TestNewHTTPClientCheckRedirectRefusesSchemeChange(t *testing.T) {
	c := NewHTTPClient()
	orig, _ := http.NewRequest(http.MethodGet, "https://example.com/a", nil)
	next, _ := http.NewRequest(http.MethodGet, "http://example.com/b", nil)
	if err := c.CheckRedirect(next, []*http.Request{orig}); err == nil {
		t.Error("https -> http redirect on the same host: want error, got nil")
	}
}

func TestNewHTTPClientCheckRedirectRefusesDifferentHost(t *testing.T) {
	c := NewHTTPClient()
	orig, _ := http.NewRequest(http.MethodGet, "http://example.com:1234/a", nil)
	next, _ := http.NewRequest(http.MethodGet, "http://evil.example.com:1234/b", nil)
	if err := c.CheckRedirect(next, []*http.Request{orig}); err == nil {
		t.Error("cross-host redirect: want error, got nil")
	}
}

func TestNewHTTPClientCheckRedirectRefusesDifferentPort(t *testing.T) {
	c := NewHTTPClient()
	orig, _ := http.NewRequest(http.MethodGet, "http://example.com:1234/a", nil)
	next, _ := http.NewRequest(http.MethodGet, "http://example.com:5678/b", nil)
	if err := c.CheckRedirect(next, []*http.Request{orig}); err == nil {
		t.Error("different-port redirect: want error, got nil")
	}
}

func TestNewHTTPClientCheckRedirectAllowsSameHost(t *testing.T) {
	c := NewHTTPClient()
	orig, _ := http.NewRequest(http.MethodGet, "http://example.com:1234/a", nil)
	next, _ := http.NewRequest(http.MethodGet, "http://example.com:1234/b", nil)
	if err := c.CheckRedirect(next, []*http.Request{orig}); err != nil {
		t.Errorf("same-host redirect: want nil, got %v", err)
	}
}

func TestDialRefusesLinkLocal(t *testing.T) {
	for addr, refused := range map[string]bool{
		"169.254.169.254:80":          true, // cloud metadata
		"169.254.1.1:8989":            true,
		"[fe80::1]:8989":              true,
		"[fe80::1%en0]:8989":          true,
		"192.168.1.10:8989":           false,
		"127.0.0.1:8989":              false,
		"10.0.0.5:7878":               false,
		"[::1]:8989":                  false,
		"[2001:db8::1]:8989":          false,
		"[fd00:ec2::254]:80":          true, // AWS metadata over IPv6
		"[fd00:ec2::253]:80":          false,
		"100.100.100.200:80":          true, // Alibaba Cloud metadata
		"100.100.100.201:80":          false,
		"[::ffff:100.100.100.200]:80": true,
	} {
		err := refuseLinkLocal("tcp", addr, nil)
		if refused && !errors.Is(err, errRefusedAddress) {
			t.Errorf("%s: err = %v, want refused", addr, err)
		}
		if !refused && err != nil {
			t.Errorf("%s: err = %v, want allowed", addr, err)
		}
	}
}

func TestClientRefusesLinkLocal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	_, err := NewHTTPClient().Do(req)
	if !errors.Is(err, errRefusedAddress) {
		t.Fatalf("request to the metadata address: %v, want refused before connecting", err)
	}
}

func TestNewHTTPClientRefusesHugeResponseHeaders(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Big", strings.Repeat("a", 100<<10))
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	base, _ := url.Parse(ts.URL)
	var out map[string]any
	err := GetJSON(context.Background(), Target{BaseURL: base, HTTP: NewHTTPClient()}, "/", "", &out)
	if err == nil || !strings.Contains(err.Error(), "header") {
		t.Fatalf("GetJSON with 100 KiB of response headers: err = %v, want a header size error", err)
	}
}
