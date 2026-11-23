package adapter

import (
	"context"
	"testing"

	"observarr/internal/shape"
)

// fakeAdapter is a minimal Adapter used only to exercise the Registry.
type fakeAdapter struct {
	info Info
}

func (f fakeAdapter) Info() Info { return f.info }

func (f fakeAdapter) Detect(ctx context.Context, t Target) (Identity, error) {
	return Identity{}, nil
}

func (f fakeAdapter) Probe(ctx context.Context, t Target, kind shape.Kind) (any, error) {
	return nil, ErrUnknownKind
}

func TestRegistryGetAndMiss(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeAdapter{info: Info{Type: "sonarr", Name: "Sonarr"}})

	a, ok := r.Get("sonarr")
	if !ok {
		t.Fatal("Get(\"sonarr\"): want ok, got not found")
	}
	if a.Info().Type != "sonarr" {
		t.Errorf("Get(\"sonarr\").Info().Type = %q, want sonarr", a.Info().Type)
	}

	if _, ok := r.Get("nope"); ok {
		t.Error("Get(\"nope\"): want not found, got found")
	}
}

func TestRegistryRegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeAdapter{info: Info{Type: "sonarr", Name: "Sonarr"}})

	defer func() {
		if recover() == nil {
			t.Error("Register with duplicate type: want panic, got none")
		}
	}()
	r.Register(fakeAdapter{info: Info{Type: "sonarr", Name: "Sonarr Again"}})
}

func TestRegistryRegisterEmptyTypePanics(t *testing.T) {
	r := NewRegistry()
	defer func() {
		if recover() == nil {
			t.Error("Register with empty type: want panic, got none")
		}
	}()
	r.Register(fakeAdapter{info: Info{Type: "", Name: "Nameless"}})
}

func TestRegistryInfosSortedByName(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeAdapter{info: Info{Type: "zeta", Name: "Zeta"}})
	r.Register(fakeAdapter{info: Info{Type: "alpha", Name: "Alpha"}})
	r.Register(fakeAdapter{info: Info{Type: "mid", Name: "Mid"}})

	infos := r.Infos()
	if len(infos) != 3 {
		t.Fatalf("Infos: got %d entries, want 3", len(infos))
	}
	want := []string{"Alpha", "Mid", "Zeta"}
	for i, name := range want {
		if infos[i].Name != name {
			t.Errorf("Infos[%d].Name = %q, want %q", i, infos[i].Name, name)
		}
	}
}

func TestRegistryInfosEmpty(t *testing.T) {
	r := NewRegistry()
	if infos := r.Infos(); len(infos) != 0 {
		t.Errorf("Infos on empty registry: got %d entries, want 0", len(infos))
	}
}

func TestRegistryInfosSortedByNameThenType(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeAdapter{info: Info{Type: "b-type", Name: "Same"}})
	r.Register(fakeAdapter{info: Info{Type: "a-type", Name: "Same"}})
	r.Register(fakeAdapter{info: Info{Type: "z-type", Name: "Alpha"}})

	infos := r.Infos()
	want := []struct{ Name, Type string }{
		{"Alpha", "z-type"},
		{"Same", "a-type"},
		{"Same", "b-type"},
	}
	if len(infos) != len(want) {
		t.Fatalf("got %d infos, want %d", len(infos), len(want))
	}
	for i, w := range want {
		if infos[i].Name != w.Name || infos[i].Type != w.Type {
			t.Errorf("infos[%d] = {Name: %q, Type: %q}, want %+v", i, infos[i].Name, infos[i].Type, w)
		}
	}
}

func TestRegistryInfosClonesKinds(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeAdapter{info: Info{
		Type: "sonarr", Name: "Sonarr",
		Kinds: []shape.Kind{shape.KindHealth, shape.KindQueue},
	}})

	infos := r.Infos()
	infos[0].Kinds[0] = "tampered"

	infos2 := r.Infos()
	if infos2[0].Kinds[0] != shape.KindHealth {
		t.Errorf("mutating one Infos() result's Kinds affected a later call: got %v", infos2[0].Kinds[0])
	}
}
