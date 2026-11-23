package discovery

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"observarr/internal/adapter"
	"observarr/internal/collect"
	"observarr/internal/store"
)

// imageApp recognises the image "fake/<type>" and fingerprints with fn.
type imageApp struct{ fingerprinter }

func (a imageApp) MatchesImage(image string) bool {
	return adapter.ImageRepo(image) == "fake/"+a.info.Type
}

func TestDocker(t *testing.T) {
	st := newStore(t)
	fp := func(_ context.Context, tg adapter.Target) (bool, string, error) {
		switch tg.BaseURL.Hostname() {
		case "tv", "relabelled", "tv.example":
			return true, "4.0", nil
		case "films":
			return false, "", nil
		}
		return false, "", errors.New("dial tcp: lookup other: no such host")
	}
	e, _ := newEngine(t, st, imageApp{fingerprinter{info("sonarr", 8989), fp}},
		imageApp{fingerprinter{info("radarr", 7878), fp}}, info("plain", 80))
	running := func(c collect.Container) collect.Container { c.State = "running"; return c }
	tcp := func(ports ...int) []collect.Port {
		var out []collect.Port
		for _, p := range ports {
			out = append(out, collect.Port{Private: p, Proto: "tcp"})
		}
		return out
	}
	cs := []collect.Container{
		running(collect.Container{Name: "tv", Image: "FAKE/sonarr:latest", Ports: tcp(8989)}),
		running(collect.Container{Name: "films", Image: "custom/thing", Labels: map[string]string{"observarr.type": "radarr"},
			Ports: append([]collect.Port{{Private: 1900, Proto: "udp"}}, tcp(9000, 7878)...)}),
		running(collect.Container{Name: "other", Image: "custom", Labels: map[string]string{"observarr.type": "radarr"},
			Ports: tcp(9000, 9001)}),
		running(collect.Container{Name: "relabelled", Image: "fake/sonarr", Labels: map[string]string{"observarr.type": "ghost"}}),
		running(collect.Container{Name: "override", Image: "fake/sonarr",
			Labels: map[string]string{"observarr.url": "https://tv.example/sonarr/"}}),
		running(collect.Container{Name: "badurl", Image: "fake/sonarr", Labels: map[string]string{"observarr.url": "ftp://x"}}),
		running(collect.Container{Name: "plain", Image: "x", Labels: map[string]string{"observarr.type": "plain"}}),
		running(collect.Container{Name: "web", Image: "nginx"}),
		{Name: "stopped", Image: "fake/sonarr", State: "exited"},
	}
	res, err := e.Docker(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Suggestion{}
	for _, s := range res.Suggestions {
		got[s.Container] = s
		if s.Source != "docker" {
			t.Errorf("%s: source %q", s.Container, s.Source)
		}
	}
	want := map[string]Suggestion{
		"tv":         {Type: "sonarr", URL: "http://tv:8989/", Verified: true, Version: "4.0"},
		"films":      {Type: "radarr", URL: "http://films:7878/", Reason: reasonOtherApp},
		"other":      {Type: "radarr", URL: "http://other:9000/", Reason: reasonUnreachable},
		"relabelled": {Type: "sonarr", URL: "http://relabelled:8989/", Verified: true, Version: "4.0"},
		"override":   {Type: "sonarr", URL: "https://tv.example/sonarr", Verified: true, Version: "4.0"},
		"plain":      {Type: "plain", URL: "http://plain:80/", Reason: reasonUnchecked},
	}
	if len(got) != len(want) {
		t.Fatalf("suggestions for %v, want %v", keys(got), keys(want))
	}
	for name, w := range want {
		g := got[name]
		if g.Type != w.Type || g.URL != w.URL || g.Verified != w.Verified || g.Version != w.Version || g.Reason != w.Reason {
			t.Errorf("%s: got %+v, want %+v", name, g, w)
		}
	}
	if len(res.Errors) != 1 || res.Errors[0].Target != "badurl" {
		t.Fatalf("errors = %+v", res.Errors)
	}
	if c, ok := e.Peek(got["tv"].ID); !ok || c.Container != "tv" {
		t.Fatalf("Peek = %+v, %v", c, ok)
	}
}

func TestDockerSkipsLinkedContainers(t *testing.T) {
	st := newStore(t)
	_, err := st.CreateService(context.Background(), store.Service{Type: "sonarr", Name: "TV", BaseURL: "http://10.0.0.5:8989",
		Container: "tv"}, func(int64) ([]byte, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	e, _ := newEngine(t, st, imageApp{fingerprinter{info("sonarr", 8989), okFor("tv", "tv2")}})
	res, err := e.Docker(context.Background(), []collect.Container{
		{Name: "tv", Image: "fake/sonarr", State: "running"},
		{Name: "tv2", Image: "fake/sonarr", State: "running"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suggestions) != 1 || res.Suggestions[0].Container != "tv2" {
		t.Fatalf("suggestions = %+v, want only the unlinked container", res.Suggestions)
	}
}

func TestDockerSkipsInvalidContainerNames(t *testing.T) {
	e, _ := newEngine(t, newStore(t), imageApp{fingerprinter{info("sonarr", 8989), okFor()}})
	var logs bytes.Buffer
	e.log = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	bad := []string{"", "-tv", ".tv", "tv@evil.example", "evil.example/x", "tv:1", "tv x", "tv\n", strings.Repeat("a", 64)}
	var cs []collect.Container
	for _, name := range append(bad, "Tv_2.lan-1", strings.Repeat("a", 63)) {
		cs = append(cs, collect.Container{Name: name, Image: "fake/sonarr", State: "running"})
	}
	var res Result
	for range 2 {
		var err error
		if res, err = e.Docker(context.Background(), cs); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(logs.String(), "skipping container with an unusable name"); n != len(bad) {
		t.Fatalf("logged %d skipped containers over two runs, want each of the %d once:\n%s", n, len(bad), logs.String())
	}
	var got []string
	for _, s := range res.Suggestions {
		got = append(got, s.Container)
	}
	slices.Sort(got)
	if want := []string{"Tv_2.lan-1", strings.Repeat("a", 63)}; !slices.Equal(got, want) || len(res.Errors) != 0 {
		t.Fatalf("suggestions for %q (errors %+v), want only %q", got, res.Errors, want)
	}
}

func TestOtherSourcesAreVerified(t *testing.T) {
	e, _ := newEngine(t, newStore(t), fingerprinter{info("sonarr", 8989), okFor("127.0.0.1")})
	res := scan(t, e, "127.0.0.1")
	if len(res.Suggestions) != 1 || !res.Suggestions[0].Verified || res.Suggestions[0].Reason != "" {
		t.Fatalf("network suggestions = %+v", res.Suggestions)
	}
}

func keys(m map[string]Suggestion) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
