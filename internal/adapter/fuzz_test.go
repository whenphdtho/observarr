package adapter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"testing/fstest"
)

func FuzzParseBaseURL(f *testing.F) {
	for _, s := range []string{"http://a", "https://a:1/x/", "http://[::1]:80", "http://u@a", "ftp://a", "http://a?b",
		"http://a/%2f/", "http://:80", "http://a:99999", "http://a b", "//a", "http://a/\x00", "http://%41"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		u, err := ParseBaseURL(s)
		if err != nil {
			return
		}
		if len(s) > MaxURL || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
			u.RawQuery != "" || u.Fragment != "" || strings.HasSuffix(u.Path, "/") {
			t.Fatalf("accepted %q as %#v", s, u)
		}
		// A request path joins onto what was accepted.
		if _, err := joinPath(u, "/api/x?y=1"); err != nil {
			t.Fatalf("joinPath on %q: %v", s, err)
		}
	})
}

func FuzzImageRepo(f *testing.F) {
	for _, s := range []string{"a", "docker.io/library/App:1@sha256:x", "lscr.io:443/linuxserver/app", "localhost/a:b",
		":", "/", "@", "a:b/c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// Docker hands over any text as an image name; what comes back only ever gets compared.
		if r := ImageRepo(s); strings.ContainsAny(r, "@") {
			t.Fatalf("ImageRepo(%q) = %q keeps a digest", s, r)
		}
	})
}

// FuzzDecoders feeds the token-stream readers adapters build on with any input and cap.
func FuzzDecoders(f *testing.F) {
	for _, s := range []string{``, `null`, `[]`, `{}`, `[1,2,3]`, `{"a":[{"b":{}}],"c":1}`, `[[[[[[`, `{"a":`, `[1,]`,
		`{"a":1}{"b":2}`, `"x"`, `[{"a":1},{"a":"x"}]`, `{"b":[[[]]],"a":null}`} {
		f.Add([]byte(s), uint8(1))
	}
	f.Fuzz(func(t *testing.T, data []byte, max uint8) {
		n := 0
		_, _ = DecodeArray(json.NewDecoder(bytes.NewReader(data)), int(max), func(json.RawMessage) { n++ })
		if n > int(max) {
			t.Fatalf("DecodeArray handed over %d elements, cap %d", n, max)
		}
		n = 0
		_, _ = CountArray(json.NewDecoder(bytes.NewReader(data)), int(max), func(json.RawMessage) { n++ })
		if n > int(max) {
			t.Fatalf("CountArray handed over %d elements, cap %d", n, max)
		}
		_ = DecodeObject(json.NewDecoder(bytes.NewReader(data)), map[string]func(*json.Decoder) error{
			"a": func(d *json.Decoder) error {
				var v json.RawMessage
				return d.Decode(&v)
			},
		})
	})
}

func FuzzReadConfigFile(f *testing.F) {
	f.Add([]byte("<Config></Config>"), false)
	f.Add([]byte{}, true)
	f.Fuzz(func(t *testing.T, data []byte, dir bool) {
		fsys := fstest.MapFS{"c": {Data: data, Mode: 0o600}}
		if dir {
			fsys = fstest.MapFS{"c/x": {Data: data}}
		}
		b, err := ReadConfigFile(fsys, "c")
		if err == nil && (dir || !bytes.Equal(b, data) || len(b) > maxConfigBytes) {
			t.Fatalf("read %d bytes (dir %v)", len(b), dir)
		}
	})
}
