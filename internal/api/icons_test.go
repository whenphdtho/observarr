package api

import (
	"context"
	"testing"

	"observarr/internal/adapter"
	"observarr/internal/config"
	"observarr/internal/shape"
)

const testLogo = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><path d="M0 0h24v24H0z"/></svg>`

// brandedApp is an adapter with a logo; it is never asked to detect or probe anything.
type brandedApp struct{ typ string }

func (b brandedApp) Info() adapter.Info {
	return adapter.Info{Type: b.typ, Name: b.typ, Kinds: []shape.Kind{shape.KindHealth}}
}
func (brandedApp) Detect(context.Context, adapter.Target) (adapter.Identity, error) {
	return adapter.Identity{}, nil
}
func (brandedApp) Probe(context.Context, adapter.Target, shape.Kind) (any, error) { return nil, nil }
func (brandedApp) Logo() []byte                                                   { return []byte(testLogo) }

// plainApp has no logo.
type plainApp struct{ brandedApp }

func (plainApp) Logo() []byte { return nil }

func TestIconIsServedWithoutSessionAndLocked(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(brandedApp{typ: "branded"})
	reg.Register(plainApp{brandedApp{typ: "plain"}})
	c := newServer(t, config.Config{}, func(s *Server) { s.Registry = reg })

	code, body, h := c.raw("GET", "/api/icons/branded.svg?v=1", "", nil)
	if code != 200 || string(body) != testLogo {
		t.Fatalf("GET icon = %d %q", code, body)
	}
	want := map[string]string{
		"Content-Type":            "image/svg+xml",
		"Content-Security-Policy": logoCSP,
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "public, max-age=31536000, immutable",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	for _, path := range []string{"/api/icons/plain.svg", "/api/icons/unknown.svg", "/api/icons/branded", "/api/icons/.svg"} {
		if code, _, _ := c.raw("GET", path, "", nil); code != 404 {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
	if code, _, _ := c.raw("POST", "/api/icons/branded.svg", "", nil); code != 404 {
		t.Errorf("POST icon = %d, want 404", code)
	}
}

func TestInfosNameTheLogoWithAVersion(t *testing.T) {
	reg := adapter.NewRegistry()
	reg.Register(brandedApp{typ: "branded"})
	reg.Register(plainApp{brandedApp{typ: "plain"}})
	for _, info := range reg.Infos() {
		switch info.Type {
		case "branded":
			if len(info.IconURL) <= len("/api/icons/branded.svg?v=") || info.IconURL[:len("/api/icons/branded.svg?v=")] != "/api/icons/branded.svg?v=" {
				t.Errorf("IconURL = %q", info.IconURL)
			}
		case "plain":
			if info.IconURL != "" {
				t.Errorf("plain IconURL = %q, want none", info.IconURL)
			}
		}
	}
}
