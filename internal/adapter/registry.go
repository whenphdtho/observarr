package adapter

import (
	"cmp"
	"fmt"
	"slices"
)

// Registry holds adapters by type. It is filled once at start and read-only afterwards.
type Registry struct{ byType map[string]Adapter }

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byType: make(map[string]Adapter)}
}

// Register adds a under its Info().Type. It panics on a duplicate or empty type: both are
// programming errors that can only happen while the catalog is assembled at start.
func (r *Registry) Register(a Adapter) {
	typ := a.Info().Type
	if typ == "" {
		panic("adapter: Register called with an adapter whose Info().Type is empty")
	}
	if _, exists := r.byType[typ]; exists {
		panic(fmt.Sprintf("adapter: Register called twice for type %q", typ))
	}
	r.byType[typ] = a
}

// Get returns the adapter registered for typ, if any.
func (r *Registry) Get(typ string) (Adapter, bool) {
	a, ok := r.byType[typ]
	return a, ok
}

// Logo returns the logo of the adapter registered for typ, if it has one.
func (r *Registry) Logo(typ string) ([]byte, bool) {
	a, ok := r.byType[typ]
	if !ok {
		return nil, false
	}
	b, ok := a.(Branded)
	if !ok {
		return nil, false
	}
	svg := b.Logo()
	return svg, len(svg) > 0
}

// Infos returns the Info of every registered adapter, sorted by Name then Type. Each Info's
// Kinds is a fresh copy, so callers can't mutate the registry's own data. A Branded adapter's
// IconURL names its logo, with a version that changes with the logo.
func (r *Registry) Infos() []Info {
	infos := make([]Info, 0, len(r.byType))
	for _, a := range r.byType {
		info := a.Info()
		info.Kinds = slices.Clone(info.Kinds)
		if svg, ok := r.Logo(info.Type); ok {
			info.IconURL = LogoPath(info.Type) + "?v=" + logoVersion(svg)
		}
		infos = append(infos, info)
	}
	slices.SortFunc(infos, func(a, b Info) int {
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.Type, b.Type)
	})
	return infos
}
