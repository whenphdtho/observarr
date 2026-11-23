// Package web embeds the built web app (web/dist) into the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built web app. Without a build it contains only .gitkeep and the server answers
// that the UI is not built.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // fixed path inside the embedded tree: cannot fail
	}
	return sub
}
