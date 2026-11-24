package crowdsec

import _ "embed"

// logo is CrowdSec's logo in one colour, its alpha the glyph. It belongs to the CrowdSec project and is used only
// to identify the app (see NOTICE).
//
//go:embed icon.svg
var logo []byte

// Logo returns CrowdSec's logo.
func (Adapter) Logo() []byte { return logo }
