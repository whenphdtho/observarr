package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"observarr/internal/auth"
	"observarr/internal/store"
)

const (
	minPasswordLen = 12   // characters
	maxPasswordLen = 1024 // bytes; bounds hashing work per request
	noPassword     = "!"  // stored hash of an account created by a trusted proxy (store.EnsureUser)

	errNeedsHTTPS  = "login needs HTTPS: on a plain-HTTP network set OBSERVARR_INSECURE_COOKIES=1; behind an HTTPS reverse proxy set OBSERVARR_TRUSTED_PROXIES to the proxy's address"
	errSetupToken  = "wrong or missing setup token — find it in the server log or in the file setup-token in the data folder"
	errViaProxy    = "sign in through the login proxy"
	proxyAuditGap  = time.Hour        // a proxy-asserted user's logins are audited at most this often
	proxyAuditKeep = 1000             // proxy-asserted users remembered for proxyAuditGap; beyond, all are forgotten
	failAuditGap   = 15 * time.Minute // failed logins (or wrong setup tokens) from one address add an audit row at most this often
	failAuditKeep  = 10_000           // addresses whose failures are counted; beyond, ended counts are forgotten, all if need be
)

// failKey groups failed attempts: one kind (the audit action), from one address, under one attempted user name.
type failKey struct{ action, addr, actor string }

// failCount counts the failed attempts of one failKey that one audit row stands for.
type failCount struct {
	row   int64     // the audit row
	since time.Time // the first of them
	count int
}

type credentials struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

// setupInput is the first account, with the setup token the server logged at start.
type setupInput struct {
	credentials
	Token string `json:"token"`
}

// sessionInfo is what the UI learns about the current session.
type sessionInfo struct {
	User  string `json:"user,omitempty"`
	CSRF  string `json:"csrf,omitempty"`
	Setup bool   `json:"setup,omitempty"` // no account exists yet: show the setup form
}

// session reports the current session. Behind a trusted login proxy it logs the asserted user in; on a fresh
// install it asks the UI for setup.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	if s.refuseDirect(w, r) {
		return
	}
	ctx := r.Context()
	proxyName, viaProxy := s.proxyUser(r)

	sess, err := s.Sessions.Lookup(r)
	switch {
	case err == nil && (!viaProxy || strings.EqualFold(proxyName, sess.UserName)):
		writeJSON(w, http.StatusOK, sessionInfo{User: sess.UserName, CSRF: sess.CSRF})
		return
	case err != nil && !errors.Is(err, store.ErrNotFound):
		s.internal(w, r, err)
		return
	}

	if viaProxy {
		u, err := s.Store.EnsureUser(ctx, proxyName)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		action := ""
		if s.auditProxyLogin(u.Name) {
			action = "logged in via trusted proxy"
		}
		s.startSession(w, r, u, action)
		return
	}
	hasUsers, err := s.Store.HasUsers(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !hasUsers {
		writeJSON(w, http.StatusOK, sessionInfo{Setup: true})
		return
	}
	writeError(w, http.StatusUnauthorized, "not logged in")
}

// auditProxyLogin reports whether a login of name via the trusted proxy is to be audited: the first one in
// proxyAuditGap is. A client that keeps no cookie gets a new session on every request, which must not flood
// the audit log.
func (s *Server) auditProxyLogin(name string) bool {
	s.proxyMu.Lock()
	defer s.proxyMu.Unlock()
	now, key := time.Now(), strings.ToLower(name)
	if at, ok := s.proxyLogins[key]; ok && now.Sub(at) < proxyAuditGap {
		return false
	}
	if s.proxyLogins == nil || len(s.proxyLogins) >= proxyAuditKeep {
		s.proxyLogins = map[string]time.Time{}
	}
	s.proxyLogins[key] = now
	return true
}

// setup creates the admin account on a fresh install and logs it in. It needs the setup token, so whoever
// reaches a fresh install first cannot claim it; a wrong token counts against the login lockout.
func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	if s.refuseDirect(w, r) || s.refuseInsecure(w, r) {
		return
	}
	var in setupInput
	if !readJSON(w, r, &in) {
		return
	}
	if !auth.ValidUserName(in.Name) {
		writeError(w, http.StatusBadRequest, "user name: 1-64 letters, digits or . _ @ -")
		return
	}
	if utf8.RuneCountInString(in.Password) < minPasswordLen || len(in.Password) > maxPasswordLen {
		writeError(w, http.StatusBadRequest, "password: at least 12 characters and at most 1024 bytes")
		return
	}
	// Refuse a finished setup before the costly hash. CreateFirstUser still decides a race atomically.
	hasUsers, err := s.Store.HasUsers(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if hasUsers {
		writeError(w, http.StatusConflict, "setup is already done")
		return
	}
	key := auth.LimitKey(auth.ClientIP(r, s.Config.TrustedProxies))
	if !s.attempt(w, key) {
		return
	}
	token := strings.ToUpper(strings.TrimSpace(in.Token)) // the token is upper-case base32; spaces come from copying
	if s.SetupToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.SetupToken)) != 1 {
		s.auditFailure(r, "(setup)", "setup token wrong", "wrong setup token", key)
		writeError(w, http.StatusForbidden, errSetupToken)
		return
	}
	var hash string
	if !s.withHashSlot(r.Context(), func() { hash, err = auth.HashPassword(in.Password) }) {
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	u, err := s.Store.CreateFirstUser(r.Context(), in.Name, hash)
	if errors.Is(err, store.ErrExists) {
		writeError(w, http.StatusConflict, "setup is already done")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.succeeded(r, key)
	if s.SetupTokenFile != "" {
		if err := os.Remove(s.SetupTokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.Log.Warn("removing the used setup token failed", "file", s.SetupTokenFile, "err", err)
		}
	}
	s.startSession(w, r, u, "created the admin account")
}

// login checks a password. Each attempt from an address counts against its lockout before the body is even
// read; only a successful login clears the count. Unknown users and password-less (trusted-proxy) accounts cost
// the same time as a wrong password, so neither can be told apart by timing.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if s.refuseDirect(w, r) || s.refuseInsecure(w, r) {
		return
	}
	key := auth.LimitKey(auth.ClientIP(r, s.Config.TrustedProxies))
	if !s.attempt(w, key) {
		return
	}
	var c credentials
	if !readJSON(w, r, &c) {
		return
	}
	if len(c.Password) > maxPasswordLen {
		writeError(w, http.StatusBadRequest, "password: at most 1024 bytes")
		return
	}
	u, err := s.Store.UserByName(r.Context(), c.Name)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internal(w, r, err)
		return
	}
	usable := err == nil && u.PasswordHash != noPassword
	hash := u.PasswordHash
	if !usable {
		hash = auth.DummyHash()
	}
	var match bool
	if !s.withHashSlot(r.Context(), func() { match = auth.CheckPassword(c.Password, hash) }) {
		return
	}
	if !match || !usable {
		actor := c.Name
		if !auth.ValidUserName(actor) {
			actor = "(invalid user name)"
		}
		s.auditFailure(r, actor, "login failed", "failed login", key)
		writeError(w, http.StatusUnauthorized, "wrong user name or password")
		return
	}
	s.succeeded(r, key)
	s.startSession(w, r, u, "logged in")
}

// succeeded tells the limiter that key logged in, and ends the address's failure counts, so later failures get
// a row of their own. Failing to remember the address only costs it the exemption from the server-wide budget,
// so the login goes ahead.
func (s *Server) succeeded(r *http.Request, key string) {
	if err := s.Limiter.Reset(r.Context(), key); err != nil {
		s.Log.Warn("remembering the address of a login failed", "err", err)
	}
	s.failMu.Lock()
	defer s.failMu.Unlock()
	for k := range s.failures {
		if k.addr == key {
			delete(s.failures, k)
		}
	}
}

// refuseDirect answers 403 when sign-in belongs to the trusted login proxy (OBSERVARR_PROXY_AUTH=1) and r does
// not come from it: neither a password nor a session cookie may get around the proxy's own sign-in.
func (s *Server) refuseDirect(w http.ResponseWriter, r *http.Request) bool {
	if !s.Config.ProxyAuth || auth.FromProxy(r, s.Config.TrustedProxies) {
		return false
	}
	writeError(w, http.StatusForbidden, errViaProxy)
	return true
}

// refuseInsecure answers 400 when a login would set a Secure cookie on a connection that is not HTTPS: the
// browser would drop the cookie, and the password would have crossed the network in clear text.
func (s *Server) refuseInsecure(w http.ResponseWriter, r *http.Request) bool {
	if !s.Config.SecureCookies || auth.IsHTTPS(r, s.Config.TrustedProxies) {
		return false
	}
	writeError(w, http.StatusBadRequest, errNeedsHTTPS)
	return true
}

// attempt reserves a login attempt for key, or answers 429 when it is locked out.
func (s *Server) attempt(w http.ResponseWriter, key string) bool {
	ok, wait := s.Limiter.Attempt(key)
	if ok {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
	minutes := max(1, int(math.Ceil(wait.Minutes())))
	writeError(w, http.StatusTooManyRequests, "too many failed logins — try again in "+strconv.Itoa(minutes)+" min")
	return false
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, sess store.Session) {
	if err := s.Sessions.End(r.Context(), w, r); err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, sess.UserName, "logged out", "")
	writeJSON(w, http.StatusOK, struct{}{})
}

// startSession logs u in and audits action, unless it is "". A session the request already carries is revoked
// first, so a replaced session does not stay valid.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User, action string) {
	if err := s.Sessions.Revoke(r.Context(), r); err != nil {
		s.internal(w, r, err)
		return
	}
	csrf, err := s.Sessions.Start(r.Context(), w, u.ID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if action != "" {
		s.audit(r, u.Name, action, "from "+auth.ClientIP(r, s.Config.TrustedProxies).String())
	}
	writeJSON(w, http.StatusOK, sessionInfo{User: u.Name, CSRF: csrf})
}

// audit records an event; a failing audit write is logged but does not fail the request.
func (s *Server) audit(r *http.Request, actor, action, detail string) {
	if _, err := s.Store.Audit(r.Context(), actor, action, detail); err != nil {
		s.Log.Error("audit write failed", "action", action, "err", err)
	}
}

// auditFailure records a failed attempt (what, e.g. "failed login") by actor from the client address addr. Only
// the first such attempt in failAuditGap adds an audit row; later ones update the count in its detail, so a flood
// of failures cannot flood the audit log.
func (s *Server) auditFailure(r *http.Request, actor, action, what, addr string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	now, key := time.Now(), failKey{action: action, addr: addr, actor: actor}
	f, ok := s.failures[key]
	if !ok || now.Sub(f.since) >= failAuditGap {
		s.makeRoomForFailures(now)
		f = failCount{since: now}
	}
	f.count++
	if f.count > 1 {
		what += "s"
	}
	detail := fmt.Sprintf("%d %s from %s in %d min", f.count, what, addr, int(failAuditGap.Minutes()))
	var err error
	if f.count == 1 {
		f.row, err = s.Store.Audit(r.Context(), actor, action, detail)
	} else {
		err = s.Store.SetAuditDetail(r.Context(), f.row, detail)
	}
	if err != nil {
		s.Log.Error("audit write failed", "action", action, "err", err)
		if f.count == 1 {
			return // no row to count in
		}
	}
	s.failures[key] = f
}

// makeRoomForFailures makes sure s.failures exists and has room for one more address: when full, it forgets
// the counts whose window has ended, and all of them if that is not enough. Caller holds failMu.
func (s *Server) makeRoomForFailures(now time.Time) {
	if s.failures == nil {
		s.failures = map[failKey]failCount{}
	}
	if len(s.failures) < failAuditKeep {
		return
	}
	for key, f := range s.failures {
		if now.Sub(f.since) >= failAuditGap {
			delete(s.failures, key)
		}
	}
	if len(s.failures) >= failAuditKeep {
		clear(s.failures)
	}
}

// withHashSlot runs fn, which hashes or checks a password, once one of the server's hash slots is free. Each
// argon2 run holds its memory cost (19 MiB for our hashes), so bounding concurrent runs bounds memory under a
// flood of logins. It reports false, without running fn, if the request ends while waiting; there is no one
// left to answer then.
func (s *Server) withHashSlot(ctx context.Context, fn func()) bool {
	select {
	case s.hashSlots <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-s.hashSlots }()
	fn()
	return true
}
