package api

import (
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// precompressed are the copies the web build writes next to its assets (web/precompress.js), best first.
var precompressed = []struct{ coding, ext string }{{"br", ".br"}, {"gzip", ".gz"}}

// ui serves the embedded web app. Existing files are served as they are (hashed assets cached for a year), or as
// their precompressed copy when the client accepts it; a missing asset is a 404; any other path (including a
// directory) gets index.html so the app can handle its own routes.
func (s *Server) ui() http.Handler {
	files := http.FileServerFS(s.UI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			// The views live in the address's fragment, so the page has one path. Anything else is a file of the
			// build or nothing: never the page under another name (a script that is gone must not come back as
			// HTML), and never a dotfile.
			if strings.HasPrefix(name, ".") || strings.Contains(name, "/.") {
				writeError(w, http.StatusNotFound, "not found")
				return
			}
			if fi, err := fs.Stat(s.UI, name); err == nil && fi.Mode().IsRegular() { // never list a directory
				// Only for the file itself: a non-canonical path is answered with a redirect, not the file.
				if r.URL.Path == "/"+name {
					if strings.HasPrefix(name, "assets/") {
						w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // file names carry a content hash
					}
					if s.serveCompressed(w, r, name) {
						return
					}
				}
				files.ServeHTTP(w, r)
				return
			}
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		index, err := fs.ReadFile(s.UI, "index.html")
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "web UI is not built: run 'make ui'")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

// serveCompressed answers with the first precompressed copy of the file name whose encoding the client accepts,
// and reports whether it did. Such an answer is always the whole file: a byte range of the compressed copy is of
// no use to a browser, so Range is ignored, which HTTP allows.
func (s *Server) serveCompressed(w http.ResponseWriter, r *http.Request, name string) bool {
	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		return false // the file server sniffs the type from the uncompressed bytes
	}
	vary := false
	for _, p := range precompressed {
		fi, err := fs.Stat(s.UI, name+p.ext)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if !vary { // a copy exists, so the uncompressed answer depends on Accept-Encoding too
			w.Header().Add("Vary", "Accept-Encoding")
			vary = true
		}
		if !accepts(r, p.coding) {
			continue
		}
		f, err := s.UI.Open(name + p.ext)
		if err != nil {
			continue
		}
		defer f.Close()
		h := w.Header()
		h.Set("Content-Type", ctype)
		h.Set("Content-Encoding", p.coding)
		h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			io.Copy(w, f)
		}
		return true
	}
	return false
}
