package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/adapters/catalog"
	"observarr/internal/shape"
)

// bodyTransport answers every request with status 200 and body, without a network.
type bodyTransport []byte

func (b bodyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		req.Body.Close()
	}
	return &http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}},
		Body:   io.NopCloser(bytes.NewReader(b)), ContentLength: int64(len(b)), Request: req}, nil
}

// configFiles are the names config readers look for; a fuzzed config folder holds each of them.
var configFiles = []string{"config.xml", "sabnzbd.ini", "nzbget.conf"}

// exercise runs everything an adapter of type typ decodes from an app (Detect, every Probe kind, and the optional
// fingerprint, linked apps and config reader) against an app that answers every request with body. Errors are
// expected; a panic or a hang is the failure.
func exercise(t *testing.T, typ string, body []byte) {
	r := adapter.NewRegistry()
	catalog.Register(r)
	a, ok := r.Get(typ)
	if !ok {
		t.Fatalf("no adapter %q", typ)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tgt := adapter.Target{BaseURL: &url.URL{Scheme: "http", Host: "app.invalid:1", Path: "/base"}, APIKey: "k:k",
		HTTP: &http.Client{Transport: bodyTransport(body)}}

	_, _ = a.Detect(ctx, tgt)
	for _, kind := range a.Info().Kinds {
		if data, err := a.Probe(ctx, tgt, kind); err == nil {
			checkReading(t, kind, data)
		}
	}
	if f, ok := a.(adapter.Fingerprinter); ok {
		_, _, _ = f.Fingerprint(ctx, tgt)
	}
	if l, ok := a.(adapter.Linker); ok {
		_, _ = l.LinkedApps(ctx, tgt)
	}
	if c, ok := a.(adapter.ConfigReader); ok {
		dir := fstest.MapFS{}
		for _, name := range configFiles {
			dir[name] = &fstest.MapFile{Data: body, Mode: 0o600}
		}
		_, _, _ = c.ReadConfig("app", dir)
	}
	if ctx.Err() != nil {
		t.Fatalf("%s took longer than 5 s on a %d-byte answer", typ, len(body))
	}
}

// maxList bounds every list a reading holds: the largest of the shape package's Max constants.
const maxList = 50

// checkReading fails t unless data is a reading the UI can show whatever the app sent: it encodes as JSON, every
// number is finite and not negative, a progress is at most 1, text is clipped and lists are bounded.
func checkReading(t *testing.T, kind shape.Kind, data any) {
	t.Helper()
	if _, err := json.Marshal(data); err != nil {
		t.Fatalf("%s reading does not encode: %v", kind, err)
	}
	var walk func(v reflect.Value, path string)
	walk = func(v reflect.Value, path string) {
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem(), path)
			}
		case reflect.Struct:
			if v.Type() == reflect.TypeFor[time.Time]() {
				return
			}
			for i := range v.NumField() {
				walk(v.Field(i), path+"."+v.Type().Field(i).Name)
			}
		case reflect.Slice:
			if v.Len() > maxList {
				t.Fatalf("%s%s holds %d entries", kind, path, v.Len())
			}
			for i := range v.Len() {
				walk(v.Index(i), path)
			}
		case reflect.String:
			if v.Len() > shape.MaxText {
				t.Fatalf("%s%s is %d bytes", kind, path, v.Len())
			}
		case reflect.Int, reflect.Int64:
			if v.Int() < 0 {
				t.Fatalf("%s%s = %d", kind, path, v.Int())
			}
		case reflect.Float64:
			f := v.Float()
			if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || (strings.HasSuffix(path, ".Progress") && f > 1) {
				t.Fatalf("%s%s = %v", kind, path, f)
			}
		}
	}
	walk(reflect.ValueOf(data), "")
}

// seeds are answers every fuzzer starts from: the edge cases of JSON and the fixtures of real apps.
var seeds = []string{
	``, `null`, `{}`, `[]`, `[null]`, `{"records":null}`, `{"records":[{}]}`, `"text"`, `-1`, `1e400`,
	`<html><body>login</body></html>`, `<Config><ApiKey>k</ApiKey><Port>-1</Port></Config>`,
	"[misc]\napi_key = k\nport = 99999\n", "ControlUsername=a\nControlPassword=b\nControlPort=0\n",
	`{"result":{"version":"1"}}`, `{"Version":"10.9","ProductName":"x","Id":"y"}`,
	`{"queue":{"slots":[{"mb":"-5","mbleft":"1e30"}]}}`, `{"pageInfo":{"results":-3},"results":[{"media":null}]}`,
}

// fixtures returns every testdata file of the adapter packages, as more seeds.
func fixtures(f *testing.F) [][]byte {
	paths, err := filepath.Glob("../*/testdata/*")
	if err != nil {
		f.Fatal(err)
	}
	var out [][]byte
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func fuzzType(f *testing.F, typ string) {
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	for _, b := range fixtures(f) {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, body []byte) { exercise(t, typ, body) })
}

func FuzzArr(f *testing.F)      { fuzzType(f, "sonarr") }
func FuzzIndexer(f *testing.F)  { fuzzType(f, "prowlarr") }
func FuzzCrowdsec(f *testing.F) { fuzzType(f, "crowdsec") }
func FuzzRclone(f *testing.F)   { fuzzType(f, "rclone") }
func FuzzSABnzbd(f *testing.F)  { fuzzType(f, "sabnzbd") }
func FuzzNZBGet(f *testing.F)   { fuzzType(f, "nzbget") }
func FuzzJellyfin(f *testing.F) { fuzzType(f, "jellyfin") }
func FuzzSeerr(f *testing.F)    { fuzzType(f, "seerr") }
