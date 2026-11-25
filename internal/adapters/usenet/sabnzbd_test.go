package usenet_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/adapters/usenet"
	"observarr/internal/shape"
)

const sabKey = "0123456789abcdef0123456789abcdef"

// fakeSAB imitates SABnzbd's API: GET /api with mode, output=json and apikey; mode=version needs no key. Any
// request with a "name" (which would make it an action) or another mode fails the test.
type fakeSAB struct {
	t        *testing.T
	version  string
	oldStyle bool // refuse a wrong key in the answer, as older versions do, rather than with a 403
	paused   bool
	free     string // diskspace1 and diskspace2, free GiB as text
	answers  map[string]string

	mu    sync.Mutex
	modes []string
}

func newFakeSAB(t *testing.T) *fakeSAB {
	now := time.Now().Unix()
	return &fakeSAB{t: t, version: "4.5.1", free: "512.40", answers: map[string]string{
		"queue": `{"queue": {"paused": %v, "kbpersec": "2048.00", "diskspace1": %q, "diskspace2": %[2]q,
			"noofslots_total": 3, "timeleft": "0:21:09", "slots": [
			{"filename": "Tears.of.Steel.2012.2160p.WEB", "status": "Downloading", "mb": "4096.00",
			 "mbleft": "1024.00", "percentage": "75", "timeleft": "0:08:32"},
			{"filename": "Pepper.and.Carrot.S01E05.1080p.WEB", "status": "Queued", "mb": "512.00",
			 "mbleft": "512.00", "percentage": "0", "timeleft": "0:12:37"},
			{"filename": "Caminandes.Llamigos.2016.1080p.WEB", "status": "Paused", "mb": 256, "mbleft": "256.00",
			 "percentage": "0", "timeleft": "0:00:00"}]}}`,
		"history": fmt.Sprintf(`{"history": {"noofslots": 3, "slots": [
			{"name": "Sintel.2010.1080p.WEB", "status": "Failed", "completed": %d,
			 "fail_message": "Repair failed, not enough repair blocks (12 short)\nmore detail"},
			{"name": "Cosmos.Laundromat.2015.1080p.WEB", "status": "Failed", "completed": %d,
			 "fail_message": "Unpacking failed, archive requires a password"},
			{"name": "Big.Buck.Bunny.2008.1080p.WEB", "status": "Failed", "completed": %d,
			 "fail_message": "Aborted, cannot be completed"}]}}`, now-600, now-7200, now-3*86400),
		"warnings": fmt.Sprintf(`{"warnings": [
			{"type": "WARNING", "text": "Old warning from last week", "time": %d, "origin": "x.py1"},
			{"type": "ERROR", "text": "Server news.example.com will be ignored for 10 minutes\nTraceback …",
			 "time": %d, "origin": "x.py2"},
			{"type": "WARNING", "text": "Too little diskspace forcing PAUSE", "time": %d, "origin": "x.py3"}]}`,
			now-7*86400, now-1200, now-60),
	}}
}

func (f *fakeSAB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	mode := q.Get("mode")
	f.mu.Lock()
	f.modes = append(f.modes, mode)
	f.mu.Unlock()
	if r.Method != http.MethodGet || r.URL.Path != "/api" || q.Has("name") || q.Get("output") != "json" {
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Redacted())
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if mode == "version" {
		fmt.Fprintf(w, `{"version": %q}`, f.version)
		return
	}
	if q.Get("apikey") != sabKey {
		if f.oldStyle {
			w.Write([]byte(`{"status": false, "error": "API Key Incorrect"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch mode {
	case "queue":
		fmt.Fprintf(w, f.answers["queue"], f.paused, f.free)
	case "history":
		if q.Get("failed_only") != "1" || q.Get("limit") == "" {
			f.t.Errorf("history without failed_only and limit: %s", r.URL.Redacted())
		}
		w.Write([]byte(f.answers["history"]))
	case "warnings":
		w.Write([]byte(f.answers["warnings"]))
	default:
		f.t.Errorf("unexpected mode %q", mode)
		w.WriteHeader(http.StatusBadRequest)
	}
}

func target(t *testing.T, h http.Handler, key string) adapter.Target {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	return adapter.Target{BaseURL: u, APIKey: key, HTTP: adapter.NewHTTPClient()}
}

func TestSABDetect(t *testing.T) {
	sab := usenet.NewSABnzbd()
	f := newFakeSAB(t)
	id, err := sab.Detect(context.Background(), target(t, f, sabKey))
	if err != nil || id.Version != "4.5.1" {
		t.Fatalf("Detect = %+v, %v", id, err)
	}

	for _, oldStyle := range []bool{false, true} {
		f := newFakeSAB(t)
		f.oldStyle = oldStyle
		if _, err := sab.Detect(context.Background(), target(t, f, "wrong")); !errors.Is(err, adapter.ErrUnauthorized) {
			t.Errorf("wrong key (old style %v): err = %v, want ErrUnauthorized", oldStyle, err)
		}
	}

	f = newFakeSAB(t)
	f.version = "2.3.9"
	if _, err := sab.Detect(context.Background(), target(t, f, sabKey)); !errors.Is(err, adapter.ErrUnsupported) {
		t.Errorf("version 2: err = %v, want ErrUnsupported", err)
	}

	for name, h := range map[string]http.HandlerFunc{
		"404":       func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"html":      func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<!doctype html><title>x</title>")) },
		"other api": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"appName": "Sonarr"}`)) },
	} {
		if _, err := sab.Detect(context.Background(), target(t, h, sabKey)); !errors.Is(err, adapter.ErrWrongApp) {
			t.Errorf("%s: err = %v, want ErrWrongApp", name, err)
		}
	}
}

func TestSABQueue(t *testing.T) {
	f := newFakeSAB(t)
	f.paused = true
	got, err := usenet.NewSABnzbd().Probe(context.Background(), target(t, f, sabKey), shape.KindQueue)
	if err != nil {
		t.Fatal(err)
	}
	q := got.(shape.Queue)
	if q.Total != 3 || len(q.Items) != 3 || q.Speed != 2048<<10 || !q.Paused {
		t.Fatalf("queue = %+v", q)
	}
	want := shape.QueueItem{Title: "Tears.of.Steel.2012.2160p.WEB", Status: "downloading", Progress: 0.75,
		SizeBytes: 4 << 30, LeftBytes: 1 << 30, ETA: "0:08:32"}
	if q.Items[0] != want {
		t.Errorf("item 0 = %+v, want %+v", q.Items[0], want)
	}
	if it := q.Items[2]; it.Status != "paused" || it.ETA != "" || it.SizeBytes != 256<<20 {
		t.Errorf("item 2 = %+v: want paused, no ETA, 256 MiB read from a number", it)
	}
}

func TestSABHealth(t *testing.T) {
	f := newFakeSAB(t)
	f.paused, f.free = true, "0.50"
	got, err := usenet.NewSABnzbd().Probe(context.Background(), target(t, f, sabKey), shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	h := got.(shape.Health)
	var msgs []string
	for _, w := range h.Warnings {
		msgs = append(msgs, string(w.Level)+": "+w.Message)
	}
	want := []string{
		"warning: downloads are paused",
		"warning: disk space low: 0.5 GiB free for downloads",
		"warning: 2 downloads failed in the last day, most recently Sintel.2010.1080p.WEB: " +
			"Repair failed, not enough repair blocks (12 short)",
		"warning: Too little diskspace forcing PAUSE",
		"error: Server news.example.com will be ignored for 10 minutes",
	}
	if h.Version != "4.5.1" || !slices.Equal(msgs, want) {
		t.Errorf("health = %+v\nwarnings %q\nwant     %q", h, msgs, want)
	}

	f = newFakeSAB(t)
	f.answers["warnings"] = `{"warnings": []}`
	f.answers["history"] = `{"history": {"slots": []}}`
	got, err = usenet.NewSABnzbd().Probe(context.Background(), target(t, f, sabKey), shape.KindHealth)
	if err != nil || len(got.(shape.Health).Warnings) != 0 {
		t.Errorf("calm health = %+v, %v; want no warnings", got, err)
	}
}

// The key travels in the query string, which net/http's errors repeat; it must never appear in an error.
func TestSABKeyNeverInErrors(t *testing.T) {
	sab := usenet.NewSABnzbd()
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/api?"+r.URL.RawQuery, http.StatusFound)
	})
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	cu, _ := url.Parse(closed.URL)
	for name, tg := range map[string]adapter.Target{
		"cross-host redirect": target(t, redirect, sabKey),
		"connection refused":  {BaseURL: cu, APIKey: sabKey, HTTP: adapter.NewHTTPClient()},
	} {
		for _, kind := range []shape.Kind{shape.KindHealth, shape.KindQueue} {
			_, err := sab.Probe(context.Background(), tg, kind)
			if err == nil || strings.Contains(err.Error(), sabKey) || strings.Contains(err.Error(), "apikey") {
				t.Errorf("%s, %s: err = %v", name, kind, err)
			}
		}
		if _, err := sab.Detect(context.Background(), tg); err == nil || strings.Contains(err.Error(), sabKey) {
			t.Errorf("%s, Detect: err = %v", name, err)
		}
	}
}

func TestSABFingerprint(t *testing.T) {
	fp := usenet.NewSABnzbd().(adapter.Fingerprinter)
	ok, version, err := fp.Fingerprint(context.Background(), target(t, newFakeSAB(t), ""))
	if !ok || version != "4.5.1" || err != nil {
		t.Errorf("Fingerprint = %v, %q, %v", ok, version, err)
	}
	ok, _, err = fp.Fingerprint(context.Background(), target(t, http.NotFoundHandler(), ""))
	if ok || err != nil {
		t.Errorf("another app: Fingerprint = %v, %v; want false, nil", ok, err)
	}
}

func TestSABReadConfig(t *testing.T) {
	ini := `__version__ = 19
__encoding__ = utf-8
[misc]
port = 8085
https_port = ""
enable_https = 0
url_base = /sabnzbd
api_key = ` + sabKey + `
nzb_key = fedcba9876543210
[servers]
[[news.example.com]]
api_key = not-this-one
port = 563
`
	r := usenet.NewSABnzbd().(adapter.ConfigReader)
	c, ok, err := r.ReadConfig("sabnzbd", fstest.MapFS{"sabnzbd.ini": {Data: []byte(ini)}})
	if err != nil || !ok {
		t.Fatalf("ReadConfig = %v, %v", ok, err)
	}
	if c.Type != "sabnzbd" || c.BaseURL.String() != "http://:8085/sabnzbd" || c.APIKey != sabKey ||
		c.KeySource != "sabnzbd.ini in sabnzbd" {
		t.Errorf("candidate = %+v (%s)", c, c.BaseURL)
	}

	https := strings.Replace(strings.Replace(ini, "enable_https = 0", "enable_https = 1", 1), `https_port = ""`,
		"https_port = 9090", 1)
	c, _, _ = r.ReadConfig("sab", fstest.MapFS{"sabnzbd.ini": {Data: []byte(https)}})
	if c.BaseURL.String() != "https://:9090/sabnzbd" {
		t.Errorf("https candidate URL = %s", c.BaseURL)
	}

	if _, ok, err := r.ReadConfig("other", fstest.MapFS{"config.xml": {Data: []byte("<Config/>")}}); ok || err != nil {
		t.Errorf("another app's folder: ok %v, err %v", ok, err)
	}
}

func TestSABMatchesImage(t *testing.T) {
	m := usenet.NewSABnzbd().(adapter.ImageMatcher)
	for _, image := range []string{"sabnzbd/sabnzbd:4.5", "lscr.io/linuxserver/sabnzbd:latest",
		"ghcr.io/hotio/sabnzbd:release", "hotio/sabnzbd"} {
		if !m.MatchesImage(image) {
			t.Errorf("%s: not matched", image)
		}
	}
	for _, image := range []string{"lscr.io/linuxserver/nzbget", "sabnzbd", "example/sabnzbd-exporter"} {
		if m.MatchesImage(image) {
			t.Errorf("%s: matched", image)
		}
	}
}
