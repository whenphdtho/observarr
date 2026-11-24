package arr

import "embed"

// icons holds each family member's logo as icons/<type>.svg: one colour, its alpha the glyph. The logos belong
// to their projects and are used only to identify the apps (see NOTICE).
//
//go:embed icons/*.svg
var icons embed.FS

// Logo returns this family member's logo, or nil if it has none.
func (a arrAdapter) Logo() []byte {
	b, err := icons.ReadFile("icons/" + a.def.Type + ".svg")
	if err != nil {
		return nil
	}
	return b
}
