package auth

import (
	"net/http"
	"net/netip"
	"testing"
)

// FuzzClientIP checks that whatever a trusted proxy forwards, the client address is the peer or an address the
// header names that is not itself a trusted proxy.
func FuzzClientIP(f *testing.F) {
	for _, s := range []string{"", "1.2.3.4", "1.2.3.4, 10.0.0.1", "::ffff:1.2.3.4", "x, 1.2.3.4", ",,,", "fe80::1%eth0"} {
		f.Add(s, "10.0.0.2:1234", "https")
	}
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	f.Fuzz(func(t *testing.T, xff, remote, proto string) {
		r := &http.Request{RemoteAddr: remote, Header: http.Header{"X-Forwarded-For": {xff},
			"X-Forwarded-Proto": {proto}}}
		a := ClientIP(r, trusted)
		peer := peerAddr(r)
		if a != peer && (inAny(trusted, a) || !inAny(trusted, peer)) {
			t.Fatalf("ClientIP = %v for peer %v and %q", a, peer, xff)
		}
		_ = IsHTTPS(r, trusted)
		_, _ = ProxyUser(r, trusted, "X-Forwarded-For")
	})
}
