package api

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"observarr/internal/auth"
	"observarr/internal/store"
)

const (
	failedWindow    = 24 * time.Hour // failed sign-ins are shown for this long
	maxFailedRows   = 5000           // audit rows of failed sign-ins read at most
	maxFailedGroups = 50             // addresses shown at most
	maxGroupUsers   = 10             // attempted user names shown per address at most
	maxLockouts     = 50
	maxSignIns      = 20
	maxSessionsShow = 100

	actionSignedOutOthers = "signed out other sessions"
)

// failureActions are the audit actions of failed attempts (auditFailure), loginActions those of sign-ins.
var (
	failureActions = []string{"login failed", "setup token wrong"}
	loginActions   = map[string]string{
		"logged in":                   "password",
		"logged in via trusted proxy": "proxy",
		"created the admin account":   "setup",
	}
)

// failureDetail matches the detail auditFailure writes: "<n> <what> from <address> in <m> min".
var failureDetail = regexp.MustCompile(`^(\d{1,9}) [a-z ]+ from (\S{1,64}) in \d{1,4} min$`)

// securityView is GET /api/security: how Observarr itself is protected and who tried to get in.
type securityView struct {
	Failed       failedSignIns      `json:"failed"`
	Lockouts     []lockout          `json:"lockouts"`
	ServerLocked int                `json:"serverLockedSec"` // the server-wide limit refuses new addresses this long still
	SignIns      []signIn           `json:"signIns"`
	Sessions     []auth.SessionInfo `json:"sessions"`
	SessionCount int                `json:"sessionCount"` // all unexpired sessions; Sessions may show fewer
	Setup        setupState         `json:"setup"`
	Settings     []accessSetting    `json:"settings"`
}

// failedSignIns are the failed sign-ins (and wrong setup tokens) of the last Hours, grouped by address.
type failedSignIns struct {
	Hours     int           `json:"hours"`
	Attempts  int           `json:"attempts"`  // failed attempts in all
	Addresses int           `json:"addresses"` // distinct addresses
	Groups    []failedGroup `json:"groups"`    // the latest maxFailedGroups addresses, latest first
	Partial   bool          `json:"partial"`   // more were recorded than were read
}

type failedGroup struct {
	Address    string    `json:"address"`
	Attempts   int       `json:"attempts"`
	Last       time.Time `json:"last"`
	Users      []string  `json:"users"`      // attempted user names, at most maxGroupUsers
	MoreUsers  int       `json:"moreUsers"`  // further names not listed
	SetupToken bool      `json:"setupToken"` // wrong setup tokens came from here
	Locked     bool      `json:"locked"`     // the address is locked out now
}

type lockout struct {
	Address  string    `json:"address"`
	Attempts int       `json:"attempts"`
	Seconds  int       `json:"seconds"` // until the lockout ends
	Until    time.Time `json:"until"`
}

type signIn struct {
	At      time.Time `json:"at"`
	User    string    `json:"user"`
	Address string    `json:"address"`
	Via     string    `json:"via"` // password, proxy or setup
}

type setupState struct {
	Pending  bool `json:"pending"`  // no account exists yet: whoever has the setup token can create one
	FileLeft bool `json:"fileLeft"` // the setup-token file is still in the data folder
}

// accessSetting is one line of the access checklist: a setting, its value and what it means here.
type accessSetting struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Value  string `json:"value"`
	State  string `json:"state"` // ok, info or warn
	Detail string `json:"detail"`
}

func (s *Server) security(w http.ResponseWriter, r *http.Request, _ store.Session) {
	ctx := r.Context()
	now := time.Now()
	addrs, serverLocked := s.Limiter.Status()

	failed, err := s.failedSignIns(r, now, addrs)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	signIns, err := s.signIns(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sessions, err := s.Sessions.List(r, maxSessionsShow)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	count, err := s.Store.CountSessions(ctx, s.Sessions.Now())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	hasUsers, err := s.Store.HasUsers(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}

	v := securityView{
		Failed:       failed,
		Lockouts:     []lockout{},
		ServerLocked: seconds(serverLocked),
		SignIns:      signIns,
		Sessions:     sessions,
		SessionCount: count,
		Setup:        setupState{Pending: !hasUsers, FileLeft: s.setupFileLeft()},
	}
	for _, a := range addrs {
		if a.Locked > 0 && len(v.Lockouts) < maxLockouts {
			v.Lockouts = append(v.Lockouts, lockout{Address: a.Key, Attempts: a.Attempts, Seconds: seconds(a.Locked),
				Until: now.Add(a.Locked).Truncate(time.Second)})
		}
	}
	v.Settings = s.accessSettings(r, v.Setup, serverLocked)
	writeJSON(w, http.StatusOK, v)
}

// securityLock is the one figure of GET /api/security the Overview shows: how long the server-wide sign-in limit
// still refuses new addresses. It reads only memory, so it can be asked often.
func (s *Server) securityLock(w http.ResponseWriter, _ *http.Request, _ store.Session) {
	_, serverLocked := s.Limiter.Status()
	writeJSON(w, http.StatusOK, map[string]int{"serverLockedSec": seconds(serverLocked)})
}

// signOutOthers ends every session but the caller's own.
func (s *Server) signOutOthers(w http.ResponseWriter, r *http.Request, sess store.Session) {
	n, err := s.Sessions.EndOthers(r)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.endOtherStreams(sess.CSRF) // they would otherwise run on until their next session check
	s.audit(r, sess.UserName, actionSignedOutOthers, fmt.Sprintf("%d sessions ended", n))
	writeJSON(w, http.StatusOK, map[string]int64{"ended": n})
}

// seconds rounds d up to whole seconds.
func seconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}

// failedSignIns groups the failed attempts of the last failedWindow by address. An audit row stands for up to
// failAuditGap of attempts and carries the time of the first, so an address's latest attempt is taken from the
// limiter when it still remembers one.
func (s *Server) failedSignIns(r *http.Request, now time.Time, addrs []auth.AddrState) (failedSignIns, error) {
	out := failedSignIns{Hours: int(failedWindow.Hours()), Groups: []failedGroup{}}
	rows, err := s.Store.AuditRows(r.Context(), failureActions, now.Add(-failedWindow), maxFailedRows+1)
	if err != nil {
		return out, err
	}
	if len(rows) > maxFailedRows {
		rows, out.Partial = rows[:maxFailedRows], true
	}
	type group struct {
		failedGroup
		users map[string]bool
	}
	groups := map[string]*group{}
	for _, row := range rows {
		m := failureDetail.FindStringSubmatch(row.Detail)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		addr, ok := cleanAddress(m[2])
		if !ok || n <= 0 {
			continue
		}
		g := groups[addr]
		if g == nil {
			g = &group{failedGroup: failedGroup{Address: addr, Users: []string{}}, users: map[string]bool{}}
			groups[addr] = g
		}
		g.Attempts += n
		out.Attempts += n
		if row.At.After(g.Last) {
			g.Last = row.At
		}
		if row.Action == "setup token wrong" {
			g.SetupToken = true
			continue
		}
		name := cleanUser(row.Actor)
		if g.users[name] {
			continue
		}
		g.users[name] = true
		if len(g.Users) < maxGroupUsers {
			g.Users = append(g.Users, name)
		} else {
			g.MoreUsers++
		}
	}
	for _, a := range addrs {
		if g := groups[a.Key]; g != nil {
			g.Locked = a.Locked > 0
			if a.Last.After(g.Last) {
				g.Last = a.Last
			}
		}
	}
	out.Addresses = len(groups)
	for _, g := range groups {
		out.Groups = append(out.Groups, g.failedGroup)
	}
	slices.SortFunc(out.Groups, func(a, b failedGroup) int {
		if c := b.Last.Compare(a.Last); c != 0 {
			return c
		}
		return cmp.Compare(a.Address, b.Address)
	})
	out.Groups = out.Groups[:min(len(out.Groups), maxFailedGroups)]
	return out, nil
}

// signIns returns the latest successful sign-ins.
func (s *Server) signIns(r *http.Request) ([]signIn, error) {
	actions := make([]string, 0, len(loginActions))
	for a := range loginActions {
		actions = append(actions, a)
	}
	rows, err := s.Store.AuditRows(r.Context(), actions, time.Time{}, maxSignIns)
	if err != nil {
		return nil, err
	}
	out := make([]signIn, 0, len(rows))
	for _, row := range rows {
		addr, ok := cleanAddress(strings.TrimPrefix(row.Detail, "from "))
		if !ok {
			addr = ""
		}
		out = append(out, signIn{At: row.At, User: cleanUser(row.Actor), Address: addr, Via: loginActions[row.Action]})
	}
	return out, nil
}

// cleanAddress returns s, read from the audit log, in canonical form if it is an address or prefix.
func cleanAddress(s string) (string, bool) {
	if a, err := netip.ParseAddr(s); err == nil {
		return a.String(), true
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.String(), true
	}
	return "", false
}

// cleanUser returns an attempted user name from the audit log as it may be shown: a valid name, or a placeholder.
func cleanUser(name string) string {
	if auth.ValidUserName(name) {
		return name
	}
	return "(invalid user name)"
}

// setupFileLeft reports whether the setup-token file is still on disk.
func (s *Server) setupFileLeft() bool {
	file := s.SetupTokenFile
	if s.Config.DataDir != "" {
		file = filepath.Join(s.Config.DataDir, "setup-token")
	}
	if file == "" {
		return false
	}
	_, err := os.Lstat(file)
	return err == nil
}

// publicAddress reports whether a is reachable from the internet: not private, loopback, link-local or in the
// shared range carrier-grade NAT and overlay networks use (100.64.0.0/10).
func publicAddress(a netip.Addr) bool {
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// accessSettings explains, for the request r, how sign-in to Observarr is protected.
func (s *Server) accessSettings(r *http.Request, setup setupState, serverLocked time.Duration) []accessSetting {
	cfg := s.Config
	https := auth.IsHTTPS(r, cfg.TrustedProxies)
	public := publicAddress(auth.ClientIP(r, cfg.TrustedProxies))
	out := []accessSetting{}

	conn := accessSetting{ID: "connection", Label: "This connection", Value: "HTTPS", State: "ok",
		Detail: "Your browser reaches Observarr encrypted."}
	if !https {
		conn.Value, conn.State = "Plain HTTP", "info"
		conn.Detail = "Unencrypted. Fine on a home network you trust; anyone on the network path can read what is sent."
		if public {
			conn.State = "warn"
			conn.Detail = "Unencrypted, from a public address: your password and session cross the internet in clear " +
				"text. Serve Observarr through an HTTPS reverse proxy."
		}
	}
	out = append(out, conn)

	cookies := accessSetting{ID: "cookies", Label: "Secure cookies", Value: "On", State: "ok",
		Detail: "The session cookie is only sent over HTTPS, and sign-in over plain HTTP is refused."}
	if !cfg.SecureCookies {
		cookies.Value, cookies.State = "Off", "info"
		cookies.Detail = "OBSERVARR_INSECURE_COOKIES=1 lets you sign in over plain HTTP. Fine on a home network you " +
			"trust; never expose this port to the internet."
		switch {
		case public && !https:
			cookies.State = "warn"
			cookies.Detail = "OBSERVARR_INSECURE_COOKIES=1 is set and you are connected from a public address: the " +
				"session cookie can be read on the way. Put Observarr behind an HTTPS reverse proxy and remove the setting."
		case https:
			cookies.State = "warn"
			cookies.Detail = "OBSERVARR_INSECURE_COOKIES=1 is set although this connection is HTTPS: remove it, so the " +
				"session cookie can never be sent unencrypted."
		}
	}
	out = append(out, cookies)

	proxies := accessSetting{ID: "proxies", Label: "Trusted proxies", Value: "None", State: "ok",
		Detail: "Observarr uses the address that connects to it. Set OBSERVARR_TRUSTED_PROXIES only behind a reverse proxy."}
	if len(cfg.TrustedProxies) == 0 && r.Header.Get("X-Forwarded-For") != "" {
		proxies.State = "warn"
		proxies.Detail = "Requests arrive through a proxy, but OBSERVARR_TRUSTED_PROXIES is not set: every sign-in " +
			"seems to come from the proxy, so one lockout locks everyone out. Set it to the proxy's address."
	}
	if n := len(cfg.TrustedProxies); n > 0 {
		names := make([]string, 0, min(n, 4))
		for _, p := range cfg.TrustedProxies[:min(n, 4)] {
			names = append(names, p.String())
		}
		list := strings.Join(names, ", ")
		if n > 4 {
			list += fmt.Sprintf(" and %d more", n-4)
		}
		proxies.Value = strconv.Itoa(n) + " set"
		proxies.Detail = "Requests from " + list + " may name the client's address and whether it used HTTPS."
		for _, p := range cfg.TrustedProxies {
			if p.Bits() < 8 || (p.Addr().Is6() && p.Bits() < 16) {
				proxies.State = "warn"
				proxies.Detail = "OBSERVARR_TRUSTED_PROXIES includes " + p.String() + ", which trusts almost any " +
					"address: a client could fake its own address and dodge the lockout. List only your proxy's address."
				break
			}
		}
	}
	out = append(out, proxies)

	proxyAuth := accessSetting{ID: "proxyAuth", Label: "Sign-in by proxy", Value: "Off", State: "ok",
		Detail: "Observarr checks passwords itself."}
	if cfg.ProxyAuth {
		proxyAuth.Value, proxyAuth.State = "On", "info"
		proxyAuth.Detail = "Your reverse proxy signs users in and names them in the " + cfg.ProxyUserHeader +
			" header. Requests that bypass the proxy are refused; keep Observarr's own port off the internet anyway."
	}
	out = append(out, proxyAuth)

	limit := accessSetting{ID: "lockout", Label: "Sign-in lockout", State: "ok",
		Value: fmt.Sprintf("%d tries / %d min", auth.MaxAttempts, int(auth.LockoutWindow.Minutes())),
		Detail: fmt.Sprintf("An address is locked out after %d attempts in %d minutes. The whole server accepts at most "+
			"%d failed attempts a minute; addresses that signed in before keep working.",
			auth.MaxAttempts, int(auth.LockoutWindow.Minutes()), auth.GlobalAttempts)}
	if serverLocked > 0 {
		limit.State = "warn"
		limit.Detail = fmt.Sprintf("Many addresses are failing to sign in: for the next %d s only addresses that "+
			"signed in before may try.", seconds(serverLocked))
	}
	out = append(out, limit)

	token := accessSetting{ID: "setup", Label: "Setup token", Value: "Used", State: "ok",
		Detail: "The admin account exists and the one-time setup token is gone."}
	switch {
	case setup.Pending:
		token.Value, token.State = "Open", "warn"
		token.Detail = "No account exists yet: whoever has the setup token can create the admin account."
	case setup.FileLeft:
		token.Value, token.State = "File left", "warn"
		token.Detail = "The setup is done, but the file setup-token is still in the data folder. Delete it."
	}
	return append(out, token)
}
