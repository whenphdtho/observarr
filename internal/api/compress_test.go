package api

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"observarr/internal/config"
	"observarr/internal/shape"
)

func TestAccepts(t *testing.T) {
	for header, want := range map[string]bool{
		"gzip": true, "deflate, gzip;q=0.5": true, " GZIP ": true, "br;q=1, gzip ; q=0.001": true,
		"": false, "gzip;q=0": false, "gzip;q=0.0": false, "*": false, "x-gzip": false, "identity": false,
		"gzip;q=nope": false, "gzip; Q=0": false,
		"gzip;level=1": true, "gzip;level=1;q=0.5": true, "gzip;level=1;q=0": false, "gzip ; level=1 ; Q=0": false,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", header)
		if got := accepts(r, "gzip"); got != want {
			t.Errorf("accepts(%q, gzip) = %v, want %v", header, got, want)
		}
	}
}

func TestBulkJSON(t *testing.T) {
	big := map[string]string{"text": strings.Repeat("observarr ", 200)}
	small := map[string]string{"text": "short"}
	for _, tc := range []struct {
		name     string
		v        any
		encoding string
		gzipped  bool
	}{
		{"big, gzip accepted", big, "gzip, br", true},
		{"big, gzip refused", big, "gzip;q=0", false},
		{"big, no header", big, "", false},
		{"small, gzip accepted", small, "gzip", false},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", tc.encoding)
		w := httptest.NewRecorder()
		writeBulkJSON(w, r, http.StatusOK, tc.v)
		h := w.Result().Header
		if h.Get("Content-Type") != "application/json" || h.Get("Cache-Control") != "no-store" ||
			h.Get("Vary") != "Accept-Encoding" || w.Code != 200 {
			t.Errorf("%s: status %d, headers %v", tc.name, w.Code, h)
		}
		body := w.Body.Bytes()
		if gotGzip := h.Get("Content-Encoding") == "gzip"; gotGzip != tc.gzipped {
			t.Fatalf("%s: gzipped = %v, want %v", tc.name, gotGzip, tc.gzipped)
		}
		if tc.gzipped {
			zr, err := gzip.NewReader(w.Body)
			if err != nil {
				t.Fatal(err)
			}
			if body, err = io.ReadAll(zr); err != nil {
				t.Fatal(err)
			}
			if w.Body.Len() >= len(body) {
				t.Errorf("%s: %d bytes compressed to %d", tc.name, len(body), w.Body.Len())
			}
		}
		want, _ := json.Marshal(tc.v)
		if string(body) != string(want)+"\n" {
			t.Errorf("%s: body %q, want %s", tc.name, body, want)
		}
	}
}

// gzipGet requests path accepting gzip itself, so the transport leaves the body as sent.
func (c *client) gzipGet(path string) (*http.Response, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest("GET", c.base+path, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		if body, err = gzip.NewReader(resp.Body); err != nil {
			c.t.Fatal(err)
		}
	}
	raw, err := io.ReadAll(body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp, raw
}

func TestLargeServiceListIsCompressedSessionNever(t *testing.T) {
	e := newServicesEnv(t)
	for i := range 12 {
		e.create(fmt.Sprintf("App %02d with a longer name", i))
	}
	resp, raw := e.gzipGet("/api/services")
	var list []map[string]any
	if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "gzip" || json.Unmarshal(raw, &list) != nil ||
		len(list) != 12 {
		t.Fatalf("GET /api/services = %d %v %s", resp.StatusCode, resp.Header, raw)
	}
	// The CSRF token must never be in a compressed response.
	resp, raw = e.gzipGet("/api/session")
	if resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "" || !strings.Contains(string(raw), `"csrf"`) {
		t.Fatalf("GET /api/session = %d %v %s", resp.StatusCode, resp.Header, raw)
	}
	resp, _ = e.gzipGet("/api/nope")
	if resp.StatusCode != 404 || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("an error answer is compressed: %d %v", resp.StatusCode, resp.Header)
	}
}

func TestLoginAndSetupNotCompressed(t *testing.T) {
	c := newServer(t, config.Config{})
	for _, req := range []struct{ path, body string }{{"/api/setup", adminSetup}, {"/api/login", admin}} {
		code, _, h := c.do("POST", req.path, req.body, map[string]string{"Accept-Encoding": "gzip"})
		if code != 200 || h.Get("Content-Encoding") != "" || c.csrf == "" {
			t.Fatalf("POST %s = %d %v", req.path, code, h)
		}
	}
}

func TestEventsCompressedAndFlushedPerEvent(t *testing.T) {
	e := newServicesEnv(t)
	id := e.create("App")
	req, _ := http.NewRequestWithContext(timeout(t), "GET", e.base+"/api/events", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" || resp.Header.Get("Vary") != "Accept-Encoding" ||
		resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("headers %v", resp.Header)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	s := &stream{t: t, resp: resp, sc: bufio.NewScanner(zr), seen: e.seen}
	s.until("snapshot", nil)
	// A change arrives on its own: the gzip stream is flushed with every event, not when its buffer fills.
	e.srv.Hub.Record(id, shape.KindHealth, shape.Reading{Error: "down for a test", At: time.Now()})
	s.until("service", func(data string) bool { return strings.Contains(data, "down for a test") })
	s.until("ping", nil)
}

func TestEventsUncompressedUnlessAccepted(t *testing.T) {
	e := newServicesEnv(t)
	req, _ := http.NewRequestWithContext(timeout(t), "GET", e.base+"/api/events", nil)
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := e.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	s := &stream{t: t, resp: resp, sc: bufio.NewScanner(resp.Body), seen: e.seen}
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("headers %v", resp.Header)
	}
	s.until("snapshot", nil)
}

func TestPrecompressedAssets(t *testing.T) {
	js := strings.Repeat("console.log(1);", 100)
	c := newServer(t, config.Config{}, func(s *Server) {
		s.UI = fstest.MapFS{
			"index.html":          {Data: []byte("<html>app</html>")},
			"assets/app-1.js":     {Data: []byte(js)},
			"assets/app-1.js.gz":  {Data: []byte("GZ")},
			"assets/app-1.js.br":  {Data: []byte("BROTLI")},
			"assets/app-1.css":    {Data: []byte("body{}")},
			"assets/app-1.css.gz": {Data: []byte("CSSGZ")},
			"assets/plain-1.js":   {Data: []byte("plain")},
		}
	})
	for _, tc := range []struct {
		method, path, accept, rng string
		body, encoding            string
		vary                      bool
	}{
		{"GET", "/assets/app-1.js", "gzip, deflate, br", "", "BROTLI", "br", true},
		{"GET", "/assets/app-1.js", "gzip", "", "GZ", "gzip", true},
		{"GET", "/assets/app-1.js", "br;q=0, gzip", "", "GZ", "gzip", true},
		{"GET", "/assets/app-1.js", "identity", "", js, "", true},
		{"GET", "/assets/app-1.js", "gzip", "bytes=0-0", "GZ", "gzip", true}, // whole file, never a range of it
		{"HEAD", "/assets/app-1.js", "br", "", "", "br", true},
		{"GET", "/assets/app-1.css", "br, gzip", "", "CSSGZ", "gzip", true},
		{"GET", "/assets/app-1.css", "br", "", "body{}", "", true},
		{"GET", "/assets/plain-1.js", "br, gzip", "", "plain", "", false},
		{"GET", "/", "br, gzip", "", "<html>app</html>", "", false},
	} {
		req, _ := http.NewRequest(tc.method, c.base+tc.path, nil)
		req.Header.Set("Accept-Encoding", tc.accept)
		if tc.rng != "" {
			req.Header.Set("Range", tc.rng)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		name := tc.method + " " + tc.path + " (" + tc.accept + ")"
		h := resp.Header
		if resp.StatusCode != 200 || string(body) != tc.body || h.Get("Content-Encoding") != tc.encoding ||
			(h.Get("Vary") == "Accept-Encoding") != tc.vary {
			t.Errorf("%s = %d %q %v", name, resp.StatusCode, body, h)
		}
		if strings.HasPrefix(tc.path, "/assets/app-1.") {
			wantType := "text/javascript; charset=utf-8"
			if strings.HasSuffix(tc.path, ".css") {
				wantType = "text/css; charset=utf-8"
			}
			if h.Get("Content-Type") != wantType || !strings.Contains(h.Get("Cache-Control"), "immutable") {
				t.Errorf("%s: type %q, cache %q", name, h.Get("Content-Type"), h.Get("Cache-Control"))
			}
		}
		wantLength := int64(len(body))
		if tc.method == "HEAD" {
			wantLength = int64(len("BROTLI"))
		}
		if tc.encoding != "" && resp.ContentLength != wantLength {
			t.Errorf("%s: Content-Length %d, want %d", name, resp.ContentLength, wantLength)
		}
	}
	// A non-canonical path is still redirected, never answered with a compressed copy.
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", c.base+"/assets/app-1.js/", nil)
	req.Header.Set("Accept-Encoding", "br")
	resp, err := noFollow.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("non-canonical path = %d %v", resp.StatusCode, resp.Header)
	}
}
