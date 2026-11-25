package usenet_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/adapters/usenet"
	"observarr/internal/shape"
)

// nzbMethods is every JSON-RPC method the adapter may call. Anything else reaching the fake fails the test.
var nzbMethods = []string{"version", "status", "listgroups", "history"}

// fakeNZB imitates NZBGet's JSON-RPC: POST /jsonrpc with basic authentication (when pass is set), the result in a
// {"version": "1.1", "result": …} envelope, and a Server header on every answer.
type fakeNZB struct {
	t          *testing.T
	user, pass string
	status     string // the status method's result
	groups     string // listgroups' result
	history    string // history's result
	refuse     bool   // answer with a JSON-RPC error
}

// nzbGroup is one listgroups item as NZBGet sends it, with some of the many fields the adapter ignores.
func nzbGroup(name, status string, size, left, paused int64, health int) string {
	return fmt.Sprintf(`{"FirstID": 1, "NZBID": 1, "NZBName": %q, "Kind": "NZB", "Status": %q,
		"FileSizeLo": %d, "FileSizeHi": %d, "FileSizeMB": %d, "RemainingSizeLo": %d, "RemainingSizeHi": %d,
		"PausedSizeLo": %d, "PausedSizeHi": %d, "Health": %d, "CriticalHealth": 900,
		"Parameters": [], "ServerStats": [], "Log": []}`,
		name, status, size&0xffffffff, size>>32, size>>20, left&0xffffffff, left>>32, paused&0xffffffff,
		paused>>32, health)
}

func nzbStatus(rate, free int64, paused bool) string {
	return fmt.Sprintf(`{"RemainingSizeLo": 0, "RemainingSizeHi": 0, "RemainingSizeMB": 0,
		"DownloadRate": %d, "DownloadRateLo": %d, "DownloadRateHi": %d, "DownloadPaused": %v,
		"ServerStandBy": false, "QuotaReached": false, "FreeDiskSpaceLo": %d, "FreeDiskSpaceHi": %d,
		"FreeDiskSpaceMB": %d, "NewsServers": [{"ID": 1, "Active": true}]}`,
		rate, rate&0xffffffff, rate>>32, paused, free&0xffffffff, free>>32, free>>20)
}

func newFakeNZB(t *testing.T) *fakeNZB {
	now := time.Now().Unix()
	return &fakeNZB{t: t, user: "nzbget", pass: "s3cret:with:colons",
		status: nzbStatus(4<<20, 200<<30, false),
		groups: "[" + strings.Join([]string{
			nzbGroup("Tears.of.Steel.2012.2160p.WEB", "DOWNLOADING", 8<<30, 6<<30, 0, 1000),
			nzbGroup("Pepper.and.Carrot.S01E05.1080p.WEB", "QUEUED", 1<<30, 1<<30, 128<<20, 1000),
			nzbGroup("Caminandes.Gran.Dillama.2013.1080p.WEB", "PAUSED", 512<<20, 512<<20, 512<<20, 1000),
			nzbGroup("Sintel.2010.1080p.WEB", "PP_QUEUED", 2<<30, 0, 0, 850),
		}, ",") + "]",
		history: fmt.Sprintf(`[
			{"ID": 9, "Name": "Big.Buck.Bunny.2008.1080p.WEB", "Status": "FAILURE/PAR", "HistoryTime": %d, "Kind": "NZB"},
			{"ID": 8, "Name": "Cosmos.Laundromat.2015.1080p.WEB", "Status": "SUCCESS/ALL", "HistoryTime": %d, "Kind": "NZB"},
			{"ID": 7, "Name": "Sintel.2010.720p.WEB", "Status": "DELETED/MANUAL", "HistoryTime": %d, "Kind": "NZB"},
			{"ID": 6, "Name": "Tears.of.Steel.2012.720p.WEB", "Status": "FAILURE/UNPACK", "HistoryTime": %d, "Kind": "NZB"},
			{"ID": 5, "Name": "Caminandes.Llama.Drama.2013.1080p.WEB", "Status": "FAILURE/HEALTH", "HistoryTime": %d, "Kind": "NZB"}
		]`, now-300, now-900, now-1800, now-7200, now-2*86400),
	}
}

func (f *fakeNZB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "nzbget-24.3")
	if r.URL.Path != "/jsonrpc" {
		f.t.Errorf("unexpected path %s", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	if f.pass != "" {
		if u, p, ok := r.BasicAuth(); !ok || u != f.user || p != f.pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="NZBGet"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	if r.Method != http.MethodPost {
		f.t.Errorf("unexpected method %s", r.Method)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Method string `json:"method"`
		Params []any  `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !slices.Contains(nzbMethods, req.Method) {
		f.t.Errorf("unexpected call %+v (%v)", req, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if f.refuse {
		w.Write([]byte(`{"version": "1.1", "error": {"name": "JSONRPCError", "code": 1, "message": "Access denied"}}`))
		return
	}
	result := map[string]string{"version": `"24.3"`, "status": f.status, "listgroups": f.groups,
		"history": f.history}[req.Method]
	fmt.Fprintf(w, "{\n\"version\" : \"1.1\",\n\"id\" : 1,\n\"result\" : %s\n}", result)
}

func (f *fakeNZB) key() string { return f.user + ":" + f.pass }

func TestNZBDetect(t *testing.T) {
	nzb := usenet.NewNZBGet()
	f := newFakeNZB(t)
	id, err := nzb.Detect(context.Background(), target(t, f, f.key()))
	if err != nil || id.Version != "24.3" {
		t.Fatalf("Detect = %+v, %v", id, err)
	}
	if _, err := nzb.Detect(context.Background(), target(t, f, "nzbget:wrong")); !errors.Is(err, adapter.ErrUnauthorized) {
		t.Errorf("wrong password: err = %v, want ErrUnauthorized", err)
	}
	if _, err := nzb.Detect(context.Background(), target(t, f, "no-colon")); !errors.Is(err, adapter.ErrCredentials) {
		t.Errorf("malformed credentials: err = %v, want ErrCredentials", err)
	}
	f.pass = ""
	if _, err := nzb.Detect(context.Background(), target(t, f, "")); err != nil {
		t.Errorf("no password set: err = %v", err)
	}

	for name, h := range map[string]http.HandlerFunc{
		"404": func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"405": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) },
		"501": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotImplemented) },
		"html": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<!doctype html><title>x</title>"))
		},
		"other json": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"version": "v1.71.2"}`)) },
	} {
		if _, err := nzb.Detect(context.Background(), target(t, h, "")); !errors.Is(err, adapter.ErrWrongApp) {
			t.Errorf("%s: err = %v, want ErrWrongApp", name, err)
		}
	}
}

func TestNZBQueue(t *testing.T) {
	f := newFakeNZB(t)
	got, err := usenet.NewNZBGet().Probe(context.Background(), target(t, f, f.key()), shape.KindQueue)
	if err != nil {
		t.Fatal(err)
	}
	q := got.(shape.Queue)
	if q.Total != 4 || len(q.Items) != 4 || q.Speed != 4<<20 || q.Paused {
		t.Fatalf("queue = %+v", q)
	}
	want := []shape.QueueItem{
		// 6 GiB at 4 MiB/s: 1536 s.
		{Title: "Tears.of.Steel.2012.2160p.WEB", Status: "downloading", Progress: 0.25, SizeBytes: 8 << 30,
			LeftBytes: 6 << 30, ETA: "0:25:36"},
		// Then 1 GiB less 128 MiB of paused repair files: 1760 s in all.
		{Title: "Pepper.and.Carrot.S01E05.1080p.WEB", Status: "queued", Progress: 0, SizeBytes: 1 << 30,
			LeftBytes: 1 << 30, ETA: "0:29:20"},
		{Title: "Caminandes.Gran.Dillama.2013.1080p.WEB", Status: "paused", Progress: 0, SizeBytes: 512 << 20,
			LeftBytes: 512 << 20},
		{Title: "Sintel.2010.1080p.WEB", Status: "waiting", Progress: 1, SizeBytes: 2 << 30,
			Problem: "too many articles are missing to repair it"},
	}
	if !slices.Equal(q.Items, want) {
		t.Errorf("items:\n%+v\nwant\n%+v", q.Items, want)
	}

	// A paused downloader has no ETAs; a queue longer than MaxQueueItems is counted in full.
	f.status = nzbStatus(0, 200<<30, true)
	many := make([]string, shape.MaxQueueItems+7)
	for i := range many {
		many[i] = nzbGroup(fmt.Sprintf("Big.Buck.Bunny.Part%02d", i), "QUEUED", 1<<20, 1<<20, 0, 1000)
	}
	f.groups = "[" + strings.Join(many, ",") + "]"
	got, err = usenet.NewNZBGet().Probe(context.Background(), target(t, f, f.key()), shape.KindQueue)
	if err != nil {
		t.Fatal(err)
	}
	q = got.(shape.Queue)
	if q.Total != shape.MaxQueueItems+7 || len(q.Items) != shape.MaxQueueItems || !q.Paused || q.Speed != 0 ||
		q.Items[0].ETA != "" {
		t.Errorf("paused long queue: total %d, %d items, paused %v, speed %d, ETA %q", q.Total, len(q.Items),
			q.Paused, q.Speed, q.Items[0].ETA)
	}
}

func TestNZBHealth(t *testing.T) {
	f := newFakeNZB(t)
	f.status = strings.Replace(nzbStatus(0, 300<<20, true), `"QuotaReached": false`, `"QuotaReached": true`, 1)
	got, err := usenet.NewNZBGet().Probe(context.Background(), target(t, f, f.key()), shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	h := got.(shape.Health)
	var msgs []string
	for _, w := range h.Warnings {
		msgs = append(msgs, w.Message)
	}
	want := []string{
		"downloads are paused",
		"the download quota is reached",
		"disk space low: 0.3 GiB free for downloads",
		"2 downloads failed in the last day, most recently Big.Buck.Bunny.2008.1080p.WEB: Repair failed",
	}
	if h.Version != "24.3" || !slices.Equal(msgs, want) {
		t.Errorf("health = %+v\nwarnings %q\nwant     %q", h, msgs, want)
	}

	// Only so much history is read; a window still full there may hold more failures.
	now := time.Now().Unix()
	items := make([]string, 250)
	for i := range items {
		items[i] = fmt.Sprintf(`{"Name": "Sintel.Part%03d", "Status": "FAILURE/MOVE", "HistoryTime": %d}`, i, now-int64(i))
	}
	f = newFakeNZB(t)
	f.history = "[" + strings.Join(items, ",") + "]"
	got, err = usenet.NewNZBGet().Probe(context.Background(), target(t, f, f.key()), shape.KindHealth)
	if err != nil {
		t.Fatal(err)
	}
	if w := got.(shape.Health).Warnings; len(w) != 1 ||
		w[0].Message != "200 downloads or more failed in the last day, most recently Sintel.Part000: Moving the files failed" {
		t.Errorf("long history: warnings %+v", w)
	}
}

func TestNZBRefusal(t *testing.T) {
	f := newFakeNZB(t)
	f.refuse = true
	_, err := usenet.NewNZBGet().Probe(context.Background(), target(t, f, f.key()), shape.KindQueue)
	if err == nil || strings.Contains(err.Error(), "Access denied") {
		t.Errorf("err = %v; want an error without the app's message", err)
	}
}

func TestNZBFingerprint(t *testing.T) {
	fp := usenet.NewNZBGet().(adapter.Fingerprinter)
	ok, version, err := fp.Fingerprint(context.Background(), target(t, newFakeNZB(t), ""))
	if !ok || version != "24.3" || err != nil {
		t.Errorf("Fingerprint = %v, %q, %v", ok, version, err)
	}
	ok, _, err = fp.Fingerprint(context.Background(), target(t, http.NotFoundHandler(), ""))
	if ok || err != nil {
		t.Errorf("another app: Fingerprint = %v, %v; want false, nil", ok, err)
	}
}

func TestNZBReadConfig(t *testing.T) {
	conf := `# NZBGet configuration
MainDir=/downloads
ControlIP=0.0.0.0
ControlPort=6790
ControlUsername=nzbget
ControlPassword=tegbzn6789
RestrictedUsername=
SecureControl=no
`
	r := usenet.NewNZBGet().(adapter.ConfigReader)
	c, ok, err := r.ReadConfig("nzbget", fstest.MapFS{"nzbget.conf": {Data: []byte(conf)}})
	if err != nil || !ok {
		t.Fatalf("ReadConfig = %v, %v", ok, err)
	}
	if c.Type != "nzbget" || c.BaseURL.String() != "http://:6790/" || c.APIKey != "nzbget:tegbzn6789" ||
		c.KeySource != "nzbget.conf in nzbget" {
		t.Errorf("candidate = %+v (%s)", c, c.BaseURL)
	}

	open := strings.Replace(conf, "ControlPassword=tegbzn6789", "ControlPassword=", 1)
	c, ok, _ = r.ReadConfig("nzbget", fstest.MapFS{"nzbget.conf": {Data: []byte(open)}})
	if !ok || c.APIKey != "" || c.KeySource != "" {
		t.Errorf("no password: candidate %+v", c)
	}
}

func TestNZBMatchesImage(t *testing.T) {
	m := usenet.NewNZBGet().(adapter.ImageMatcher)
	for _, image := range []string{"nzbgetcom/nzbget:v24.3", "lscr.io/linuxserver/nzbget:latest",
		"ghcr.io/nzbgetcom/nzbget"} {
		if !m.MatchesImage(image) {
			t.Errorf("%s: not matched", image)
		}
	}
	if m.MatchesImage("sabnzbd/sabnzbd") {
		t.Error("sabnzbd/sabnzbd matched")
	}
}
