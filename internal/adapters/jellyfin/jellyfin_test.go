package jellyfin

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
	"reflect"
	"strings"
	"sync"
	"testing"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

const testKey = "0123456789abcdef0123456789abcdef"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeServer answers like Jellyfin: GET only, the public info without a key, everything else only with the key in
// the Authorization header. answers is by path; any other path fails the test.
type fakeServer struct {
	t       *testing.T
	answers map[string][]byte
	status  map[string]int

	mu   sync.Mutex
	seen []string
}

func newFake(t *testing.T) (*fakeServer, adapter.Target) {
	f := &fakeServer{t: t, status: map[string]int{}, answers: map[string][]byte{
		"/System/Info/Public": fixture(t, "public_info.json"),
		"/System/Info":        fixture(t, "system_info.json"),
		"/Sessions":           fixture(t, "sessions.json"),
	}}
	ts := httptest.NewServer(f)
	t.Cleanup(ts.Close)
	u, _ := url.Parse(ts.URL)
	return f, adapter.Target{BaseURL: u, APIKey: testKey, HTTP: adapter.NewHTTPClient()}
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen = append(f.seen, r.URL.RequestURI())
	f.mu.Unlock()
	body, ok := f.answers[r.URL.Path]
	if !ok {
		f.t.Errorf("unexpected call %s %s", r.Method, r.URL.RequestURI())
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		f.t.Errorf("%s %s: not a GET", r.Method, r.URL.Path)
	}
	if r.Header.Get("X-Emby-Token") != "" || r.URL.Query().Has("api_key") {
		f.t.Errorf("%s: key sent in a deprecated form", r.URL.Path)
	}
	auth := r.Header.Get("Authorization")
	if r.URL.Path == "/System/Info/Public" {
		if auth != "" {
			f.t.Errorf("the public info was asked for with a key")
		}
	} else if auth != `MediaBrowser Token="`+testKey+`"` {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if s := f.status[r.URL.Path]; s != 0 {
		w.WriteHeader(s)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(body)
}

func TestInfo(t *testing.T) {
	info := New().Info()
	if info.Type != "jellyfin" || info.DefaultPort != 8096 || !info.NeedsAPIKey ||
		!reflect.DeepEqual(info.Kinds, []shape.Kind{shape.KindHealth, shape.KindStreams}) {
		t.Errorf("Info() = %+v", info)
	}
	if err := adapter.CheckLogo(logo); err != nil {
		t.Errorf("logo: %v", err)
	}
}

func TestDetect(t *testing.T) {
	_, target := newFake(t)
	id, err := New().Detect(context.Background(), target)
	if err != nil || id.Version != "10.11.3" {
		t.Fatalf("Detect = %+v, %v", id, err)
	}
}

func TestDetectRejectsWrongKey(t *testing.T) {
	_, target := newFake(t)
	target.APIKey = "deadbeef"
	if _, err := New().Detect(context.Background(), target); !errors.Is(err, adapter.ErrUnauthorized) {
		t.Fatalf("Detect = %v, want ErrUnauthorized", err)
	}
}

func TestDetectRefusesUnsafeKey(t *testing.T) {
	f, target := newFake(t)
	for _, key := range []string{"", `abc" Other="x`, "abc\\", "ab cd", "ab\r\ncd", "äbc"} {
		target.APIKey = key
		if _, err := New().Detect(context.Background(), target); !errors.Is(err, ErrKey) {
			t.Errorf("Detect with key %q = %v, want ErrKey", key, err)
		}
	}
	if len(f.seen) != 0 {
		t.Errorf("calls made with an unusable key: %v", f.seen)
	}
}

func TestDetectWrongApp(t *testing.T) {
	cases := map[string]struct {
		body   string
		status int
		want   string
	}{
		"emby":        {body: `{"ServerName":"x","Version":"4.9.3.0","Id":"y"}`, want: adapter.ErrWrongApp.Error()},
		"named":       {body: `{"ProductName":"Other Server","Version":"1.0"}`, want: "Other Server answers at this address"},
		"not json":    {body: `<html>login</html>`, want: adapter.ErrWrongApp.Error()},
		"not found":   {status: http.StatusNotFound, want: adapter.ErrWrongApp.Error()},
		"auth needed": {status: http.StatusUnauthorized, want: adapter.ErrWrongApp.Error()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if c.status != 0 {
					w.WriteHeader(c.status)
				}
				fmt.Fprint(w, c.body)
			}))
			defer ts.Close()
			u, _ := url.Parse(ts.URL)
			target := adapter.Target{BaseURL: u, APIKey: testKey, HTTP: adapter.NewHTTPClient()}
			_, err := New().Detect(context.Background(), target)
			if !errors.Is(err, adapter.ErrWrongApp) || err.Error() != c.want {
				t.Fatalf("Detect = %v, want %q", err, c.want)
			}
			ok, _, err := New().(adapter.Fingerprinter).Fingerprint(context.Background(), target)
			if ok || err != nil {
				t.Fatalf("Fingerprint = %v, %v; want no match", ok, err)
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	f, target := newFake(t)
	target.APIKey = ""
	ok, version, err := New().(adapter.Fingerprinter).Fingerprint(context.Background(), target)
	if !ok || version != "10.11.3" || err != nil {
		t.Fatalf("Fingerprint = %v, %q, %v", ok, version, err)
	}
	if !reflect.DeepEqual(f.seen, []string{"/System/Info/Public"}) {
		t.Errorf("calls = %v", f.seen)
	}
}

func TestProbeHealth(t *testing.T) {
	_, target := newFake(t)
	got, err := New().Probe(context.Background(), target, shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	h := got.(shape.Health)
	want := []shape.Warning{{Level: shape.LevelWarning, Message: "a restart is pending to apply changes"}}
	if h.Version != "10.11.3" || !reflect.DeepEqual(h.Warnings, want) {
		t.Errorf("health = %+v", h)
	}
}

func TestProbeHealthCalm(t *testing.T) {
	f, target := newFake(t)
	f.answers["/System/Info"] = []byte(`{"Version":"10.10.7","HasPendingRestart":false,"IsShuttingDown":false}`)
	got, err := New().Probe(context.Background(), target, shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	if h := got.(shape.Health); h.Warnings == nil || len(h.Warnings) != 0 {
		t.Errorf("warnings = %#v, want []", h.Warnings)
	}
}

func TestProbeStreams(t *testing.T) {
	f, target := newFake(t)
	got, err := New().Probe(context.Background(), target, shape.KindStreams)
	if err != nil {
		t.Fatal(err)
	}
	want := shape.Streams{Total: 3, Paused: 1, Transcodes: 1, Bitrate: 8_160_000 + 10_000_000 + 3_200_000,
		Streams: []shape.Stream{
			{User: "alex", Client: "Jellyfin Web", Device: "Firefox", Title: "Big Buck Bunny (2008)", Progress: 354.0 / 596,
				Method: shape.PlayDirect, Reasons: []string{}, VideoCodec: "h264", AudioCodec: "aac", Bitrate: 8_160_000},
			{User: "robin", Client: "Jellyfin iOS", Device: "iPhone", Title: "Caminandes S01E02 · Gran Dillama", Paused: true,
				Progress: 0.4, Method: shape.PlayDirectStream, Reasons: []string{"audio codec not supported"},
				VideoCodec: "h264", AudioCodec: "flac", ToAudioCodec: "aac", Bitrate: 3_200_000},
			{User: "sam", Client: "Jellyfin Android TV", Device: "Living room TV", Title: "Sintel (2010)",
				Progress: 265.2 / 888, Method: shape.PlayTranscode,
				Reasons:    []string{"video codec not supported", "audio codec not supported", "bitrate too high"},
				VideoCodec: "hevc", AudioCodec: "truehd", ToVideoCodec: "h264", ToAudioCodec: "aac", Bitrate: 10_000_000},
		}}
	s := got.(shape.Streams)
	for i := range s.Streams {
		if d := s.Streams[i].Progress - want.Streams[i].Progress; d > 1e-9 || d < -1e-9 {
			t.Errorf("stream %d progress = %v, want %v", i, s.Streams[i].Progress, want.Streams[i].Progress)
		}
		s.Streams[i].Progress = want.Streams[i].Progress
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("streams =\n%+v\nwant\n%+v", s, want)
	}
	if !reflect.DeepEqual(f.seen, []string{"/Sessions?activeWithinSeconds=960"}) {
		t.Errorf("calls = %v", f.seen)
	}
	// The clients' addresses never reach the reading.
	b, _ := json.Marshal(s)
	for _, ip := range []string{"198.51.100.23", "203.0.113.7", "192.0.2.44"} {
		if strings.Contains(string(b), ip) {
			t.Errorf("reading holds the client address %s", ip)
		}
	}
}

func TestProbeStreamsCapsTheList(t *testing.T) {
	f, target := newFake(t)
	var b strings.Builder
	b.WriteString("[")
	for i := range shape.MaxStreams + 5 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"UserName":"u%03d","PlayState":{"PlayMethod":"Transcode"},"NowPlayingItem":{"Name":"Sintel"},`+
			`"TranscodingInfo":{"Bitrate":1000}}`, i)
	}
	b.WriteString("]")
	f.answers["/Sessions"] = []byte(b.String())
	got, err := New().Probe(context.Background(), target, shape.KindStreams)
	if err != nil {
		t.Fatal(err)
	}
	s := got.(shape.Streams)
	if s.Total != shape.MaxStreams+5 || s.Transcodes != s.Total || s.Bitrate != int64(s.Total)*1000 ||
		len(s.Streams) != shape.MaxStreams || s.Streams[0].User != "u000" {
		t.Errorf("Total %d, Transcodes %d, Bitrate %d, %d listed, first %q", s.Total, s.Transcodes, s.Bitrate,
			len(s.Streams), s.Streams[0].User)
	}
}

func TestProbeStreamsNone(t *testing.T) {
	f, target := newFake(t)
	f.answers["/Sessions"] = []byte(`[]`)
	got, err := New().Probe(context.Background(), target, shape.KindStreams)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(got); string(b) != `{"total":0,"paused":0,"transcodes":0,"bitrate":0,"streams":[]}` {
		t.Errorf("reading = %s", b)
	}
}

func TestProbeUnknownKind(t *testing.T) {
	_, target := newFake(t)
	if _, err := New().Probe(context.Background(), target, shape.KindQueue); !errors.Is(err, adapter.ErrUnknownKind) {
		t.Errorf("Probe(queue) = %v", err)
	}
}

func TestReasons(t *testing.T) {
	cases := map[string][]string{
		`["VideoRangeTypeNotSupported","ContainerBitrateExceedsLimit"]`: {"video range type not supported", "bitrate too high"},
		`"ContainerNotSupported, AudioChannelsNotSupported"`:            {"container not supported", "audio channels not supported"},
		`null`:            {},
		`42`:              {},
		`["A","A","",""]`: {"a"},
	}
	for raw, want := range cases {
		if got := reasons(json.RawMessage(raw)); !reflect.DeepEqual(got, want) {
			t.Errorf("reasons(%s) = %#v, want %#v", raw, got, want)
		}
	}
	many := make([]string, 20)
	for i := range many {
		many[i] = fmt.Sprintf("Reason%dX", i)
	}
	raw, _ := json.Marshal(many)
	if got := reasons(raw); len(got) != shape.MaxStreamReasons {
		t.Errorf("%d reasons kept, want %d", len(got), shape.MaxStreamReasons)
	}
}

func TestTitle(t *testing.T) {
	n := func(i int) *int { return &i }
	cases := []struct {
		it   item
		want string
	}{
		{item{Type: "Episode", SeriesName: "Caminandes", ParentIndexNumber: n(1), IndexNumber: n(3), Name: "Llamigos"},
			"Caminandes S01E03 · Llamigos"},
		{item{Type: "Episode", SeriesName: "Pepper & Carrot", Name: "The Potion Contest"}, "Pepper & Carrot · The Potion Contest"},
		{item{Type: "Episode", SeriesName: "Show", ParentIndexNumber: n(12), IndexNumber: n(104)}, "Show S12E104"},
		{item{Type: "Movie", Name: "Tears of Steel", ProductionYear: 2012}, "Tears of Steel (2012)"},
		{item{Type: "Movie", Name: "Cosmos Laundromat"}, "Cosmos Laundromat"},
		{item{Type: "Audio", Name: "Divider", AlbumArtist: "Chris Zabriskie"}, "Chris Zabriskie · Divider"},
		{item{Type: "TvChannel", Name: "News"}, "News"},
	}
	for _, c := range cases {
		if got := title(&c.it); got != c.want {
			t.Errorf("title(%+v) = %q, want %q", c.it, got, c.want)
		}
	}
}

func TestMatchesImage(t *testing.T) {
	a := New().(adapter.ImageMatcher)
	for _, img := range []string{"jellyfin/jellyfin:10.11.3", "lscr.io/linuxserver/jellyfin:latest", "hotio/jellyfin:release",
		"ghcr.io/hotio/jellyfin", "docker.io/jellyfin/jellyfin@sha256:abc", "ghcr.io/jellyfin/jellyfin"} {
		if !a.MatchesImage(img) {
			t.Errorf("MatchesImage(%q) = false", img)
		}
	}
	for _, img := range []string{"emby/embyserver", "jellyfin", "lscr.io/linuxserver/jellyseerr", "fallenbagel/jellyseerr"} {
		if a.MatchesImage(img) {
			t.Errorf("MatchesImage(%q) = true", img)
		}
	}
}
