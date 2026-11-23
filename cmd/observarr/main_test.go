package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"observarr/internal/config"
)

func TestHealthURL(t *testing.T) {
	cases := []struct {
		listen  string
		want    string
		wantErr bool
	}{
		{listen: ":9898", want: "http://127.0.0.1:9898/api/health"},
		{listen: "0.0.0.0:9898", want: "http://127.0.0.1:9898/api/health"},
		{listen: "[::]:9898", want: "http://127.0.0.1:9898/api/health"},
		{listen: "192.0.2.5:9898", want: "http://192.0.2.5:9898/api/health"},
		{listen: "[::1]:9898", want: "http://[::1]:9898/api/health"},
		{listen: "bad", wantErr: true},
	}
	for _, c := range cases {
		got, err := healthURL(c.listen)
		if c.wantErr {
			if err == nil {
				t.Errorf("healthURL(%q): want error, got %q", c.listen, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("healthURL(%q): unexpected error: %v", c.listen, err)
			continue
		}
		if got != c.want {
			t.Errorf("healthURL(%q) = %q, want %q", c.listen, got, c.want)
		}
	}
}

func TestRequestHeadersBounded(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {})
	srv := httptest.NewUnstartedServer(ok)
	srv.Config = newHTTPServer(context.Background(), ok)
	srv.Start()
	defer srv.Close()
	for size, want := range map[int]int{16 << 10: http.StatusOK, 64 << 10: http.StatusRequestHeaderFieldsTooLarge} {
		req, _ := http.NewRequest("GET", srv.URL, nil)
		req.Header.Set("X-Big", strings.Repeat("a", size))
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%d KiB of request headers = %d, want %d", size>>10, resp.StatusCode, want)
		}
	}
}

func TestCollectorWarnsAboutADirectSocket(t *testing.T) {
	for host, warn := range map[string]bool{"unix:///var/run/docker.sock": true, "tcp://proxy:2375": false, "": false} {
		var logs bytes.Buffer
		if _, err := collector(config.Config{DockerHost: host}, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
			t.Fatalf("%q: %v", host, err)
		}
		if got := strings.Count(logs.String(), "Docker socket mounted directly"); got != map[bool]int{true: 1}[warn] {
			t.Errorf("%q: warned %d times\n%s", host, got, logs.String())
		}
	}
}
