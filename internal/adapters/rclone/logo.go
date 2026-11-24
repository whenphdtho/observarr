package rclone

import _ "embed"

// logo is rclone's logo in one colour, its alpha the glyph. It belongs to the rclone project and is used only to
// identify the app (see NOTICE).
//
//go:embed icon.svg
var logo []byte

// Logo returns rclone's logo.
func (rcAdapter) Logo() []byte { return logo }
