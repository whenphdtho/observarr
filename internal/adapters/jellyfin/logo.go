package jellyfin

import _ "embed"

// logo is Jellyfin's logo in one colour, its alpha the glyph. It belongs to the Jellyfin project and is used only
// to identify the app (see NOTICE).
//
//go:embed icon.svg
var logo []byte

// Logo returns Jellyfin's logo.
func (jfAdapter) Logo() []byte { return logo }
