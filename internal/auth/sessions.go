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

// SessionInfo is a session as a list shows it: no token, and nothing that could stand in for one.
type SessionInfo struct {
	User    string    `json:"user"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	Current bool      `json:"current"` // the session of the request that asked
}

// List returns the unexpired sessions of every user, at most limit, newest first, marking r's own.
func (m *Sessions) List(r *http.Request, limit int) ([]SessionInfo, error) {
	rows, err := m.Store.ListSessions(r.Context(), m.Now(), limit)
	if err != nil {
		return nil, err
	}
	own := m.ownHash(r)
	out := make([]SessionInfo, 0, len(rows))
	for _, row := range rows {
		out = append(out, SessionInfo{User: row.UserName, Created: row.Expires.Add(-SessionTTL), Expires: row.Expires,
			Current: own != nil && string(row.TokenHash) == string(own)})
	}
	return out, nil
}

// EndOthers ends every unexpired session but r's own, of every user, and returns how many it ended. r must have a
// session.
func (m *Sessions) EndOthers(r *http.Request) (int64, error) {
	own := m.ownHash(r)
	if own == nil {
		return 0, store.ErrNotFound
	}
	return m.Store.DeleteSessionsExcept(r.Context(), own, m.Now())
}

// ownHash is the hash of r's session token, or nil if r carries none.
func (m *Sessions) ownHash(r *http.Request) []byte {
	c, err := r.Cookie(m.CookieName())
	if err != nil || c.Value == "" {
		return nil
	}
	return hashToken(c.Value)
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
