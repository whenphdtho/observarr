package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func postTarget(t *testing.T, serverURL string) Target {
	t.Helper()
	base, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	return Target{BaseURL: base, APIKey: "user:secret", HTTP: NewHTTPClient()}
}

func TestPostJSONSendsBodyAndCredentials(t *testing.T) {
	var method, path, ctype, body, user, pass string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, ctype = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		user, pass, _ = r.BasicAuth()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()

	var out struct{ OK bool }
	err := PostJSON(context.Background(), postTarget(t, ts.URL+"/rc"), "/core/stats", []byte(`{"group":"x"}`),
		func(r *http.Request) { r.SetBasicAuth("user", "secret") },
		func(d *json.Decoder) error { return d.Decode(&out) })
	if err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPost || path != "/rc/core/stats" || ctype != "application/json" || body != `{"group":"x"}` {
		t.Errorf("got %s %s %q %q", method, path, ctype, body)
	}
	if user != "user" || pass != "secret" || !out.OK {
		t.Errorf("auth %q/%q, ok %v", user, pass, out.OK)
	}
}

func TestPostJSONSendsNoKeyHeaderOfItsOwn(t *testing.T) {
	var auth, key string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, key = r.Header.Get("Authorization"), r.Header.Get("X-Api-Key")
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	err := PostJSON(context.Background(), postTarget(t, ts.URL), "/x", []byte(`{}`), nil,
		func(d *json.Decoder) error { return d.Decode(&json.RawMessage{}) })
	if err != nil || auth != "" || key != "" {
		t.Errorf("err %v, Authorization %q, X-Api-Key %q", err, auth, key)
	}
}

func TestPostJSONErrors(t *testing.T) {
	for code, want := range map[int]error{http.StatusUnauthorized: ErrUnauthorized, http.StatusNotFound: ErrNotFound,
		http.StatusInternalServerError: StatusError(500)} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{"error":"user:secret"}`))
		}))
		err := PostJSON(context.Background(), postTarget(t, ts.URL), "/x", []byte(`{}`),
			func(r *http.Request) { r.SetBasicAuth("user", "secret") },
			func(d *json.Decoder) error { return d.Decode(&json.RawMessage{}) })
		ts.Close()
		if !errors.Is(err, want) {
			t.Errorf("%d: err = %v, want %v", code, err, want)
		}
		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%d: error %q holds the credentials", code, err)
		}
	}
}

func TestPostJSONRefusesLargeBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request sent")
	}))
	defer ts.Close()
	big := []byte(`"` + strings.Repeat("a", maxPostBytes) + `"`)
	if err := PostJSON(context.Background(), postTarget(t, ts.URL), "/x", big, nil,
		func(*json.Decoder) error { return nil }); err == nil {
		t.Error("no error")
	}
}
