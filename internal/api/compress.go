package api

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// minGzip is the smallest JSON body worth compressing; below it gzip's own overhead eats most of the gain.
const minGzip = 1 << 10

var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

// accepts reports whether the request's Accept-Encoding names coding with a non-zero q-value. A wildcard is
// not honoured: sending an encoding only when it is named is always safe.
func accepts(r *http.Request, coding string) bool {
	for _, field := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		params := strings.Split(field, ";")
		if !strings.EqualFold(strings.TrimSpace(params[0]), coding) {
			continue
		}
		for _, p := range params[1:] {
			if q, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(p)), "q="); ok {
				v, err := strconv.ParseFloat(strings.TrimSpace(q), 64)
				return err == nil && v > 0
			}
		}
		return true // no q-value means q=1
	}
	return false
}

// writeBulkJSON is writeJSON for the large, frequently read responses: compressed with gzip when the client
// accepts it and the body is large enough to gain from it.
//
// Only for responses that hold no secret. A compressed size that depends on both a secret and text an attacker
// can place in the same response gives the secret away bit by bit (BREACH); that is why the session, setup and
// login answers, which carry the CSRF token, and the discovery results, whose suggestion ids grant the use of
// found keys, always go through writeJSON.
func writeBulkJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	b, _ := json.Marshal(v) // the values sent this way always encode
	b = append(b, '\n')     // as json.Encoder in writeJSON
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Accept-Encoding")
	if len(b) < minGzip || !accepts(r, "gzip") {
		w.WriteHeader(status)
		w.Write(b)
		return
	}
	h.Set("Content-Encoding", "gzip")
	w.WriteHeader(status)
	zw := gzipWriters.Get().(*gzip.Writer)
	defer func() {
		zw.Reset(io.Discard) // a pooled writer must not keep the response alive
		gzipWriters.Put(zw)
	}()
	zw.Reset(w)
	zw.Write(b)
	zw.Close()
}
