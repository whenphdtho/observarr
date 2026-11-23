package api

import (
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"observarr/internal/auth"
	"observarr/internal/store"
)

// securityHeaders sets strict browser security headers on every response.
func securityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// recoverPanics logs a panic in a handler and answers 500 instead of dropping the connection. If the response
// had already started, a 500 can no longer be sent, so the response is aborted instead of being corrupted.
func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &startedWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				log.Error("handler panic", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				if sw.started {
					panic(http.ErrAbortHandler)
				}
				writeError(w, http.StatusInternalServerError, "internal error")
			}
		}()
		next.ServeHTTP(sw, r)
	})
}

// startedWriter records whether a response has started.
type startedWriter struct {
	http.ResponseWriter
	started bool
}

func (w *startedWriter) WriteHeader(code int) {
	w.started = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *startedWriter) Write(b []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer for anything but Flush (deadlines, etc.).
func (w *startedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// FlushError flushes the real writer. http.ResponseController checks for this method before falling
// back to Unwrap, so a handler that only flushes (without ever calling Write) still marks the response
// as started.
func (w *startedWriter) FlushError() error {
	w.started = true
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// requireSession runs next only for a logged-in user. State-changing requests must also carry the session's
// CSRF token, and with proxy login the request must come from the proxy and the session belong to the user it
// asserts.
func (s *Server) requireSession(next func(http.ResponseWriter, *http.Request, store.Session)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.refuseDirect(w, r) {
			return
		}
		sess, err := s.Sessions.Lookup(r)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if name, ok := s.proxyUser(r); ok && !strings.EqualFold(name, sess.UserName) {
			writeError(w, http.StatusUnauthorized, "session belongs to another user")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRF)) != 1 {
			writeError(w, http.StatusForbidden, "missing or wrong CSRF token")
			return
		}
		next(w, r, sess)
	})
}

// proxyUser returns the user a trusted login proxy asserts; only with OBSERVARR_PROXY_AUTH=1.
func (s *Server) proxyUser(r *http.Request) (string, bool) {
	if !s.Config.ProxyAuth {
		return "", false
	}
	return auth.ProxyUser(r, s.Config.TrustedProxies, s.Config.ProxyUserHeader)
}

// internal logs err and answers 500 without exposing details, or 503 when the database takes no writes.
func (s *Server) internal(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	if reason := store.Degraded(err); reason != "" {
		writeError(w, http.StatusServiceUnavailable, reason+": changes cannot be saved")
		return
	}
	writeError(w, http.StatusInternalServerError, "internal error")
}
