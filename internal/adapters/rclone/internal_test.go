package rclone

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"observarr/internal/adapter"
)

// TestCallRefusesOtherPaths checks the allow-list itself: a call that changes something is never sent.
func TestCallRefusesOtherPaths(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	tg := adapter.Target{BaseURL: u, HTTP: adapter.NewHTTPClient()}
	for _, p := range []string{"mount/unmount", "mount/unmountall", "vfs/refresh", "vfs/forget", "core/quit",
		"operations/purge", "sync/copy", "config/create", "options/set", "core/command", "/core/version",
		"core/version/../core/quit"} {
		err := call(context.Background(), tg, p, nil, func(*json.Decoder) error { return nil })
		if !errors.Is(err, errNotAllowed) {
			t.Errorf("%s: err = %v, want errNotAllowed", p, err)
		}
	}
	if requests != 0 {
		t.Errorf("%d requests reached the server", requests)
	}
}

func TestCacheMode(t *testing.T) {
	cases := map[string]string{`0`: "off", `1`: "minimal", `2`: "writes", `3`: "full", `7`: "", `-1`: "",
		`"full"`: "full", `"Writes"`: "writes", `"bogus"`: "", `null`: "", ``: ""}
	for in, want := range cases {
		if got := cacheMode(json.RawMessage(in)); got != want {
			t.Errorf("cacheMode(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestSizeBytes(t *testing.T) {
	cases := map[string]int64{`107374182400`: 107374182400, `-1`: 0, `0`: 0, `"off"`: 0, `"10Gi"`: 10 << 30,
		`"512M"`: 512 << 20, `"1.5T"`: 3 << 39, `"100"`: 100 << 10, `"2048b"`: 2048, `"20GiB"`: 20 << 30,
		`"nonsense"`: 0, `true`: 0, `1e300`: 0}
	for in, want := range cases {
		if got := sizeBytes(json.RawMessage(in)); got != want {
			t.Errorf("sizeBytes(%s) = %d, want %d", in, got, want)
		}
	}
}

func TestBaseName(t *testing.T) {
	cases := map[string]string{"media-crypt:": "media-crypt:", "media-crypt:[1]": "media-crypt:",
		"cloud:dir[x]": "cloud:dir[x]", "[2]": "[2]", "/mnt/a[10]": "/mnt/a"}
	for in, want := range cases {
		if got := baseName(in); got != want {
			t.Errorf("baseName(%q) = %q, want %q", in, got, want)
		}
	}
}
