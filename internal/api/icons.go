package api

import (
	"net/http"
	"strconv"
	"strings"
)

// logoCSP is the policy of a served logo: an SVG opened on its own may draw and nothing else.
const logoCSP = "default-src 'none'; style-src 'unsafe-inline'"

// icon serves an adapter's one-colour logo (GET /api/icons/{type}.svg). Logos are part of the binary and say
// nothing about this server, so no session is needed; the URL the UI uses carries a version, so it may be cached
// for long.
func (s *Server) icon(w http.ResponseWriter, r *http.Request) {
	typ, ok := strings.CutSuffix(r.PathValue("file"), ".svg")
	if !ok || typ == "" {
		writeError(w, http.StatusNotFound, "no such icon")
		return
	}
	svg, ok := s.Registry.Logo(typ)
	if !ok {
		writeError(w, http.StatusNotFound, "no such icon")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "image/svg+xml")
	h.Set("Content-Length", strconv.Itoa(len(svg)))
	h.Set("Content-Security-Policy", logoCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(svg)
	}
}
