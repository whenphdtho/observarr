package usenet

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"observarr/internal/adapter"
)

// TestNZBCallRefusesOtherMethods checks the allow-list itself: a call that changes something is never sent.
func TestNZBCallRefusesOtherMethods(t *testing.T) {
	requests := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer ts.Close()
	u, _ := url.Parse(ts.URL)
	tg := adapter.Target{BaseURL: u, HTTP: adapter.NewHTTPClient()}
	for _, m := range []string{"pause", "resume", "shutdown", "reload", "editqueue", "append", "scan",
		"saveconfig", "rate", "writelog", "config", "log", ""} {
		err := nzbCall(context.Background(), tg, m, nil, nzbAnswerBytes, func(*json.Decoder) error { return nil })
		if !errors.Is(err, errNotAllowed) {
			t.Errorf("%q: err = %v, want errNotAllowed", m, err)
		}
	}
	if requests != 0 {
		t.Errorf("%d requests reached the server", requests)
	}
}

func TestConfigValues(t *testing.T) {
	body := []byte(`top = 1
# comment
[Misc]
Port = 8080
api_key = "quoted"
port = 9999
; comment
[[sub]]
api_key = sub
[other]
port = 1
`)
	if got, want := configValues(body, "misc", "port", "api_key", "missing"),
		map[string]string{"port": "8080", "api_key": "quoted"}; !maps.Equal(got, want) {
		t.Errorf("misc = %v, want %v", got, want)
	}
	if got := configValues(body, "", "top", "port"); !maps.Equal(got, map[string]string{"top": "1"}) {
		t.Errorf("top level = %v", got)
	}
}

func TestFailuresWarning(t *testing.T) {
	now := time.Now()
	at := now.Unix() - 60
	cases := []struct {
		add     int
		atLeast bool
		want    string
	}{
		{0, false, ""},
		{1, false, "Sintel failed in the last day: Repair failed"},
		{1, true, "1 download or more failed in the last day, most recently Sintel: Repair failed"},
		{3, false, "3 downloads failed in the last day, most recently Sintel: Repair failed"},
	}
	for _, c := range cases {
		f := newFailures(now)
		for range c.add {
			f.add(at, "Sintel", "Repair failed")
		}
		f.atLeast = c.atLeast
		w, ok := f.warning()
		if ok != (c.want != "") || w.Message != c.want {
			t.Errorf("%d failures (at least %v): %q, %v; want %q", c.add, c.atLeast, w.Message, ok, c.want)
		}
	}
	f := newFailures(now)
	if f.add(now.Add(-failedWindow).Unix()-1, "Sintel", "") {
		t.Error("a failure before the window was counted")
	}
}

func TestClock(t *testing.T) {
	for sec, want := range map[int64]string{0: "0:00:00", 59: "0:00:59", 3600: "1:00:00", 90061: "25:01:01"} {
		if got := clock(sec); got != want {
			t.Errorf("clock(%d) = %q, want %q", sec, got, want)
		}
	}
}
