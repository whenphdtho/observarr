package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
)

// maxJSONBody bounds every JSON request body.
const maxJSONBody = 64 << 10

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// readJSON decodes the request body into v and writes the error response itself when it fails.
// Requiring application/json also blocks cross-site form posts: a browser can only send that type
// cross-origin after a CORS preflight, which Observarr never allows.
func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "request body must be JSON")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	err := dec.Decode(v)
	if err == nil {
		// Exactly one value: anything after it would be silently ignored otherwise.
		switch extra := dec.Decode(&json.RawMessage{}); {
		case errors.Is(extra, io.EOF):
			return true
		case extra == nil:
			err = errors.New("unexpected data after the JSON value")
		default:
			err = extra
		}
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
	} else {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
	}
	return false
}
