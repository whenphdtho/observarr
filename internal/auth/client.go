package auth

import (
	"net/http"
	"net/netip"
	"regexp"
	"strings"
)

var userNamePattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)

// ValidUserName reports whether name is an acceptable account name.
func ValidUserName(name string) bool { return userNamePattern.MatchString(name) }

// ClientIP returns the client's address. A directly connecting peer is trusted only as itself. Behind a
// trusted proxy — one that must set or append to X-Forwarded-For — it is the first hop read from the right
// that is not itself a trusted proxy; an unparseable hop, or hops that are all trusted, fall back to the TCP
// peer. Headers from an untrusted peer are ignored outright, so none of this can be used to spoof an address.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer := peerAddr(r)
	if !inAny(trusted, peer) {
		return peer
	}
	var hops []string
	for _, line := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(line, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return peer
		}
		a = a.Unmap()
		if !inAny(trusted, a) {
			return a
		}
	}
	return peer
}

// FromProxy reports whether r comes directly from a trusted proxy.
func FromProxy(r *http.Request, trusted []netip.Prefix) bool { return inAny(trusted, peerAddr(r)) }

// ProxyUser returns the user name a trusted login proxy asserts in header. It reports false unless the request
// comes directly from a trusted proxy, the header appears exactly once (a duplicate could be the real proxy's
// value shadowed, or shadowing, an attacker-supplied one) and the name is valid.
func ProxyUser(r *http.Request, trusted []netip.Prefix, header string) (string, bool) {
	if !FromProxy(r, trusted) {
		return "", false
	}
	values := r.Header.Values(header)
	if len(values) != 1 {
		return "", false
	}
	name := strings.TrimSpace(values[0])
	return name, ValidUserName(name)
}

// IsHTTPS reports whether the browser reached Observarr over HTTPS: directly over TLS, or through a trusted
// proxy whose X-Forwarded-Proto says https. A proxy that appends to the header adds its own view last, so the
// last value counts; the header from an untrusted peer is ignored.
func IsHTTPS(r *http.Request, trusted []netip.Prefix) bool {
	if r.TLS != nil {
		return true
	}
	if !FromProxy(r, trusted) {
		return false
	}
	values := r.Header.Values("X-Forwarded-Proto")
	if len(values) == 0 {
		return false
	}
	last := values[len(values)-1]
	last = last[strings.LastIndex(last, ",")+1:]
	return strings.EqualFold(strings.TrimSpace(last), "https")
}

// LimitKey returns the key under which login attempts from a are grouped for rate limiting. An IPv6 address is
// reduced to its /64: a single client commonly rotates through many addresses within its /64 (e.g. privacy
// extensions), so limiting by the exact address would not limit anything. An IPv4 address is used unchanged.
func LimitKey(a netip.Addr) string {
	if a.Is4() || a.Is4In6() {
		return a.Unmap().String()
	}
	if p, err := a.Prefix(64); err == nil {
		return p.String()
	}
	return a.String()
}

func peerAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

func inAny(prefixes []netip.Prefix, a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
