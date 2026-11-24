package discovery

import (
	"context"
	"fmt"
	"io/fs"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/collect"
)

// localApp recognises the image "fake/<type>" and reads the app.conf of a folder named <type> (port 8989, no host,
// like reader); it counts every Fingerprint call.
type localApp struct {
	imageApp
	calls *atomic.Int32
}

func (a localApp) ReadConfig(name string, dir fs.FS) (adapter.Candidate, bool, error) {
	if name != a.info.Type { // only its own folder
		return adapter.Candidate{}, false, nil
	}
	return reader{a.fakeApp}.ReadConfig(name, dir)
}

func newLocalApp(typ string, port int) localApp {
	calls := &atomic.Int32{}
	return localApp{imageApp{fingerprinter{info(typ, port), func(context.Context, adapter.Target) (bool, string, error) {
		calls.Add(1)
		return true, "1.0", nil
	}}}, calls}
}

func running(name, image string, ports ...int) collect.Container {
	c := collect.Container{Name: name, Image: image, State: "running"}
	for _, p := range ports {
		c.Ports = append(c.Ports, collect.Port{Private: p, Proto: "tcp"})
	}
	return c
}

func TestLocalContactsNoApp(t *testing.T) {
	tv, films := newLocalApp("sonarr", 8989), newLocalApp("radarr", 7878)
	e, _ := newEngine(t, newStore(t), tv, films)
	root := onDisk(t, fstest.MapFS{"sonarr/app.conf": {Data: []byte(foundKey)}})
	cs := []collect.Container{
		running("Sonarr", "fake/sonarr", 8989),
		running("radarr", "fake/radarr", 7878),
		running("web", "nginx", 80),
		{Name: "old", Image: "fake/radarr", State: "exited"},
	}
	res, err := e.Local(context.Background(), cs, root)
	if err != nil {
		t.Fatal(err)
	}
	if n := tv.calls.Load() + films.calls.Load(); n != 0 {
		t.Fatalf("Local fingerprinted %d times, want none", n)
	}
	got := map[string]Suggestion{}
	for _, s := range res.Suggestions {
		got[s.Container] = s
	}
	if len(got) != 2 {
		t.Fatalf("suggestions = %+v", res.Suggestions)
	}
	// The config folder's key goes with the one container of its type, port and name (ignoring case).
	if s := got["Sonarr"]; s.URL != "http://Sonarr:8989/" || !s.HostKnown || !s.KeyFound || s.KeySource != "app.conf in sonarr" ||
		s.Verified || s.Reason != reasonNotYet || s.Source != "docker" || s.Type != "sonarr" {
		t.Fatalf("tv = %+v", s)
	}
	if s := got["radarr"]; s.URL != "http://radarr:7878/" || s.KeyFound || s.Verified || s.Source != "docker" {
		t.Fatalf("films = %+v", s)
	}
	if c, ok := e.Peek(got["Sonarr"].ID); !ok || c.APIKey != foundKey || c.Container != "Sonarr" {
		t.Fatalf("Peek = %+v, %v", c, ok)
	}

	// Listing again keeps the ids, so what the UI shows can still be added.
	again, err := e.Local(context.Background(), cs, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range again.Suggestions {
		if got[s.Container].ID != s.ID {
			t.Fatalf("%s: id changed on a second listing", s.Container)
		}
	}
	if len(e.Suggestions()) != 2 {
		t.Fatalf("stored = %+v", e.Suggestions())
	}
}

func TestLocalJoinsOnlyWhenUnambiguous(t *testing.T) {
	tv := newLocalApp("sonarr", 8989)
	e, _ := newEngine(t, newStore(t), tv)
	root := onDisk(t, fstest.MapFS{"sonarr/app.conf": {Data: []byte(foundKey)}})
	cases := map[string][]collect.Container{
		"two containers": {running("sonarr", "fake/sonarr", 8989), running("sonarr2", "fake/sonarr", 8989)},
		"other port":     {running("sonarr", "fake/sonarr", 9000)},
		"other name":     {running("tv", "fake/sonarr", 8989)},
		"labelled url": {{Name: "sonarr", Image: "fake/sonarr", State: "running",
			Labels: map[string]string{"observarr.url": "http://tv.example:8989/"}}},
	}
	for name, cs := range cases {
		res, err := e.Local(context.Background(), cs, root)
		if err != nil {
			t.Fatal(err)
		}
		config := 0
		for _, s := range res.Suggestions {
			if s.Source == "config" {
				config++
				if s.HostKnown || !s.KeyFound {
					t.Errorf("%s: config find = %+v", name, s)
				}
			} else if s.KeyFound {
				t.Errorf("%s: container %s got the folder's key", name, s.Container)
			}
		}
		if config != 1 {
			t.Errorf("%s: %d config finds, want 1 on its own", name, config)
		}
	}
}

func TestLocalWithoutSources(t *testing.T) {
	e, _ := newEngine(t, newStore(t), newLocalApp("sonarr", 8989))
	res, err := e.Local(context.Background(), nil, "")
	if err != nil || len(res.Suggestions) != 0 || len(res.Errors) != 0 {
		t.Fatalf("Local = %+v, %v", res, err)
	}
	res, err = e.Local(context.Background(), nil, "/nonexistent-observarr-test")
	if err != nil || len(res.Errors) != 1 || res.Errors[0].Message != "the config folder cannot be read" {
		t.Fatalf("Local with a missing folder = %+v, %v", res, err)
	}
}

// A listing never replaces a suggestion confirmed by contacting the app; it keeps it offered for longer.
func TestLocalKeepsVerifiedSuggestion(t *testing.T) {
	e, clk := newEngine(t, newStore(t), newLocalApp("sonarr", 8989))
	verified := scan(t, e, "tv").Suggestions[0]
	if !verified.Verified || verified.Version != "1.0" {
		t.Fatalf("scan = %+v", verified)
	}
	clk.add(50 * time.Minute)
	res, err := e.Local(context.Background(), []collect.Container{running("tv", "fake/sonarr", 8989)}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Suggestions) != 1 || res.Suggestions[0] != verified {
		t.Fatalf("Local = %+v, want the verified %+v", res.Suggestions, verified)
	}
	clk.add(50 * time.Minute)
	if got := e.Suggestions(); len(got) != 1 || got[0] != verified {
		t.Fatalf("after its first hour = %+v", got)
	}
}

// A host-less find matches no saved address, so it is matched to a saved service by its key, or, when keys cannot
// tell, only when there is one of each.
func TestLocalSkipsHostlessFindOfASavedService(t *testing.T) {
	tv := newLocalApp("sonarr", 8989)
	keyed := onDisk(t, fstest.MapFS{"sonarr/app.conf": {Data: []byte(foundKey)}})
	keyless := onDisk(t, fstest.MapFS{"sonarr/app.conf": {}})
	cases := []struct {
		name  string
		saved []string // the keys of the saved services
		root  string
		want  int
	}{
		{"same key", []string{"other", foundKey}, keyed, 0},
		{"other key", []string{"other"}, keyed, 1},
		{"no saved key, one service", []string{""}, keyed, 0},
		{"no saved key, two services", []string{"", ""}, keyed, 1},
		{"no found key, one service", []string{"other"}, keyless, 0},
		{"no found key, two services", []string{"other", "more"}, keyless, 1},
	}
	for _, c := range cases {
		st := newStore(t)
		for i, key := range c.saved {
			saveService(t, st, "sonarr", fmt.Sprintf("TV %d", i), fmt.Sprintf("http://tv%d:8989", i), key)
		}
		e, _ := newEngine(t, st, tv)
		res, err := e.Local(context.Background(), nil, c.root)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Suggestions) != c.want {
			t.Errorf("%s: %d suggestions, want %d", c.name, len(res.Suggestions), c.want)
		}
	}
}
