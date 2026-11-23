package auth

import (
	"crypto/tls"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

var proxies = []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")}

func TestClientIPIgnoresHeaderFromUntrustedPeer(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "198.51.100.7:4321"
	r.Header.Set("X-Forwarded-For", "203.0.113.1")
	if got := ClientIP(r, proxies); got.String() != "198.51.100.7" {
		t.Fatalf("ClientIP = %s, want the TCP peer", got)
	}
}

func TestClientIPBehindTrustedProxyUsesLastHop(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.20.0.3:4321"
	r.Header.Set("X-Forwarded-For", "10.9.9.9, 203.0.113.1") // first entry is client-supplied and untrusted
	if got := ClientIP(r, proxies); got.String() != "203.0.113.1" {
		t.Fatalf("ClientIP = %s, want the address added by the proxy", got)
	}
}

func TestClientIPWalksPastMultipleTrustedHops(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.20.0.3:4321"                           // trusted proxy 1, the TCP peer
	r.Header.Set("X-Forwarded-For", "203.0.113.1, 172.20.0.4") // client, trusted proxy 2
	if got := ClientIP(r, proxies); got.String() != "203.0.113.1" {
		t.Fatalf("ClientIP = %s, want the original client past both trusted hops", got)
	}
}

func TestClientIPUnparseableHopReturnsPeer(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.20.0.3:4321"
	r.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := ClientIP(r, proxies); got.String() != "172.20.0.3" {
		t.Fatalf("ClientIP = %s, want the peer when a hop does not parse", got)
	}
}

func TestClientIPAllHopsTrustedReturnsPeer(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.20.0.3:4321"
	r.Header.Set("X-Forwarded-For", "172.20.0.4, 172.20.0.5")
	if got := ClientIP(r, proxies); got.String() != "172.20.0.3" {
		t.Fatalf("ClientIP = %s, want the peer when every hop is trusted", got)
	}
}

func TestClientIPMultipleHeaderLines(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.20.0.3:4321"
	r.Header.Add("X-Forwarded-For", "198.51.100.9")
	r.Header.Add("X-Forwarded-For", "203.0.113.1, 172.20.0.4")
	if got := ClientIP(r, proxies); got.String() != "203.0.113.1" {
		t.Fatalf("ClientIP = %s, want the untrusted hop from the joined lines", got)
	}
}

func TestProxyUser(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Remote-User", "alice")

	r.RemoteAddr = "198.51.100.7:4321"
	if _, ok := ProxyUser(r, proxies, "Remote-User"); ok {
		t.Fatal("header from an untrusted peer must be ignored")
	}
	r.RemoteAddr = "172.20.0.3:4321"
	if name, ok := ProxyUser(r, proxies, "Remote-User"); !ok || name != "alice" {
		t.Fatalf("ProxyUser = %q, %v", name, ok)
	}
	r.Header.Set("Remote-User", "alice; DROP")
	if _, ok := ProxyUser(r, proxies, "Remote-User"); ok {
		t.Fatal("invalid user names must be rejected")
	}
	if _, ok := ProxyUser(r, nil, "Remote-User"); ok {
		t.Fatal("without trusted proxies the header must be ignored")
	}
}

func TestProxyUserRejectsDuplicateHeader(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.20.0.3:4321"
	r.Header.Add("Remote-User", "alice")
	r.Header.Add("Remote-User", "mallory")
	if _, ok := ProxyUser(r, proxies, "Remote-User"); ok {
		t.Fatal("duplicate header lines must be rejected")
	}
}

func TestValidUserName(t *testing.T) {
	for name, want := range map[string]bool{"admin": true, "a.b-c_d@e": true, "": false, "with space": false,
		strings.Repeat("x", 64): true, strings.Repeat("x", 65): false} {
		if got := ValidUserName(name); got != want {
			t.Errorf("ValidUserName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestLimitKey(t *testing.T) {
	if got := LimitKey(netip.MustParseAddr("203.0.113.5")); got != "203.0.113.5" {
		t.Fatalf("LimitKey(IPv4) = %s, want the address unchanged", got)
	}
	if got := LimitKey(netip.MustParseAddr("2001:db8::1")); got != "2001:db8::/64" {
		t.Fatalf("LimitKey(IPv6) = %s, want its /64", got)
	}
	// Two addresses in the same /64 must share a key.
	a := LimitKey(netip.MustParseAddr("2001:db8::1"))
	b := LimitKey(netip.MustParseAddr("2001:db8::ffff"))
	if a != b {
		t.Fatalf("LimitKey disagreed within one /64: %s vs %s", a, b)
	}
}

func TestIsHTTPS(t *testing.T) {
	cases := []struct {
		peer, proto string
		tls, want   bool
	}{
		{peer: "198.51.100.7:1", tls: true, want: true},
		{peer: "198.51.100.7:1", want: false},
		{peer: "198.51.100.7:1", proto: "https", want: false}, // untrusted peer: header ignored
		{peer: "172.20.0.3:1", proto: "https", want: true},
		{peer: "172.20.0.3:1", proto: "HTTPS", want: true},
		{peer: "172.20.0.3:1", proto: "http", want: false},
		{peer: "172.20.0.3:1", proto: "https, http", want: false}, // the proxy appended what it saw last
		{peer: "172.20.0.3:1", proto: "http, https", want: true},
		{peer: "172.20.0.3:1", want: false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.peer
		if c.proto != "" {
			r.Header.Set("X-Forwarded-Proto", c.proto)
		}
		if c.tls {
			r.TLS = &tls.ConnectionState{}
		}
		if got := IsHTTPS(r, proxies); got != c.want {
			t.Errorf("IsHTTPS(peer %s, proto %q, tls %v) = %v, want %v", c.peer, c.proto, c.tls, got, c.want)
		}
	}
}
