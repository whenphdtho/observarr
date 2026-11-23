package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"time"

	"observarr/internal/store"
)

// SessionTTL is how long a session lasts.
const SessionTTL = 7 * 24 * time.Hour

// Sessions creates, finds and ends browser sessions. Only a hash of the cookie token is stored, so a leaked
// database does not leak usable sessions.
type Sessions struct {
	Store  *store.Store
	Secure bool             // set the cookie's Secure flag (HTTPS); off only for plain-HTTP LAN setups
	Now    func() time.Time // clock, time.Now in production
}

// Start creates a session for userID, sets its cookie on w and returns the CSRF token the UI must send
// with every state-changing request.
func (m *Sessions) Start(ctx context.Context, w http.ResponseWriter, userID int64) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", err
	}
	expires := m.Now().Add(SessionTTL)
	if err := m.Store.CreateSession(ctx, hashToken(token), userID, csrf, expires); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{Name: m.CookieName(), Value: token, Path: "/", Expires: expires,
		HttpOnly: true, Secure: m.Secure, SameSite: http.SameSiteStrictMode})
	return csrf, nil
}

// CookieName is the session cookie's name. A Secure cookie gets the __Host- prefix, with which browsers refuse
// the cookie unless it is Secure, host-only and for path /, so neither a plain-HTTP response nor a sibling
// subdomain can plant or overwrite it.
func (m *Sessions) CookieName() string {
	if m.Secure {
		return "__Host-observarr_session"
	}
	return "observarr_session"
}

// Lookup returns the request's session, store.ErrNotFound if it has none (or an expired or unknown one),
// or another error if the database failed.
func (m *Sessions) Lookup(r *http.Request) (store.Session, error) {
	c, err := r.Cookie(m.CookieName())
	if err != nil || c.Value == "" {
		return store.Session{}, store.ErrNotFound
	}
	return m.Store.SessionByToken(r.Context(), hashToken(c.Value), m.Now())
}

// Revoke deletes the request's session row, if it has one, without touching cookies: for a session about to
// be replaced, whose new cookie overwrites the old one.
func (m *Sessions) Revoke(ctx context.Context, r *http.Request) error {
	if c, err := r.Cookie(m.CookieName()); err == nil {
		return m.Store.DeleteSession(ctx, hashToken(c.Value))
	}
	return nil
}

// End deletes the request's session and expires its cookie.
func (m *Sessions) End(ctx context.Context, w http.ResponseWriter, r *http.Request) error {
	if err := m.Revoke(ctx, r); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: m.CookieName(), Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: m.Secure, SameSite: http.SameSiteStrictMode})
	return nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
