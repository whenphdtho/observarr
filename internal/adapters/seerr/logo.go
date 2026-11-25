package seerr

import _ "embed"

// logo is Overseerr's logo in one colour, its alpha the glyph; Jellyseerr and Seerr carry the same mark. It belongs
// to the Overseerr project and is used only to identify the app (see NOTICE).
//
//go:embed icon.svg
var logo []byte

// Logo returns the apps' logo.
func (Adapter) Logo() []byte { return logo }
