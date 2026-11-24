package catalog_test

import (
	"testing"

	"observarr/internal/adapter"
	"observarr/internal/adapters/catalog"
)

func TestRegisterRegistersUniqueTypes(t *testing.T) {
	r := adapter.NewRegistry()
	catalog.Register(r)

	infos := r.Infos()
	if len(infos) != 6 {
		t.Fatalf("got %d adapters, want 6", len(infos))
	}

	seen := make(map[string]bool, len(infos))
	for _, info := range infos {
		if info.Type == "" {
			t.Errorf("info %+v has an empty Type", info)
		}
		if seen[info.Type] {
			t.Errorf("duplicate type %q in catalog", info.Type)
		}
		seen[info.Type] = true
	}

	for _, want := range []string{"sonarr", "radarr", "lidarr", "prowlarr", "crowdsec", "rclone"} {
		if !seen[want] {
			t.Errorf("catalog is missing type %q", want)
		}
	}
}

func TestRegisterCanLookUpEachType(t *testing.T) {
	r := adapter.NewRegistry()
	catalog.Register(r)

	for _, typ := range []string{"sonarr", "radarr", "lidarr", "prowlarr", "crowdsec", "rclone"} {
		a, ok := r.Get(typ)
		if !ok {
			t.Errorf("Get(%q): not found", typ)
			continue
		}
		if a.Info().Type != typ {
			t.Errorf("Get(%q).Info().Type = %q", typ, a.Info().Type)
		}
	}
}

// TestRegisterDoesNotRegisterReadarr locks in the removal of Readarr, which was retired upstream
// in 2025 and is not in the spec's supported app list.
func TestRegisterDoesNotRegisterReadarr(t *testing.T) {
	r := adapter.NewRegistry()
	catalog.Register(r)
	if _, ok := r.Get("readarr"); ok {
		t.Error("catalog still registers \"readarr\", which was retired upstream")
	}
}

// Every shipped adapter carries its app's logo, and every logo is a safe, self-contained SVG of modest size.
func TestEveryAdapterLogoIsSafeSVG(t *testing.T) {
	r := adapter.NewRegistry()
	catalog.Register(r)
	for _, info := range r.Infos() {
		svg, ok := r.Logo(info.Type)
		if !ok {
			t.Errorf("%s has no logo", info.Type)
			continue
		}
		if err := adapter.CheckLogo(svg); err != nil {
			t.Errorf("%s logo: %v", info.Type, err)
		}
		if len(svg) > 12<<10 {
			t.Errorf("%s logo is %d bytes, want at most 12 KiB", info.Type, len(svg))
		}
		if info.IconURL == "" {
			t.Errorf("%s has no IconURL", info.Type)
		}
	}
}
