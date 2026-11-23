// Package discovery suggests services: it turns user-provided scan input into bounded, validated
// host lists and runs the network, linked-app and config-folder sources behind the Engine.
package discovery

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"
)

// MaxHosts bounds how many hosts ParseTargets will ever return, so one scan request can never
// expand into an unbounded, or even just impractically large, address space.
const MaxHosts = 1024

// minIPv4Bits and minIPv6Bits are the smallest (most host-rich) CIDR prefix ParseTargets accepts
// for each address family. /22 is exactly MaxHosts addresses; /120 is 256, comfortably below it.
const (
	minIPv4Bits = 22
	minIPv6Bits = 120
)

// privateSpace lists the only ranges a CIDR token may expand within. A single hostname or IP
// still passes through unexpanded regardless of where it points — the user is naming one address
// they intend to reach, and the adapter HTTP client's own link-local refusal plus the scan's own
// per-host dial failures bound the actual risk there. A CIDR is different: it turns one short
// token into many probed addresses, so letting it range over public space would let a short
// textarea entry scan a network the user does not own.
var privateSpace = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // CGNAT
	netip.MustParsePrefix("fc00::/7"),      // ULA
	netip.MustParsePrefix("::1/128"),
}

// ParseTargets turns user input (comma/space/newline separated hostnames, IPs, CIDRs) into hosts
// to scan: hostnames pass through as-is; single IPs pass through normalised to their canonical
// text form (so e.g. "2001:DB8::1" and "2001:db8::1" collapse to one entry, and so a single IP
// that is also a member of an expanded CIDR is recognised as the same host); CIDRs expand to
// every host they contain, but only within private/loopback/CGNAT/unique-local space, and only
// down to /22 (IPv4) or /120 (IPv6). The result has no duplicates and keeps the order tokens were
// seen in. An error names the specific token that could not be parsed or was out of bounds, or,
// as soon as the unique host count exceeds MaxHosts, stops right there rather than finishing the
// expansion: a request for many more hosts than one scan will ever run must fail fast, not spend
// time and memory building a list that is just going to be discarded.
func ParseTargets(input string) ([]string, error) {
	hosts := []string{}
	seen := make(map[string]bool)
	expandedCIDRs := make(map[netip.Prefix]bool)

	for _, tok := range splitTokens(input) {
		expanded, err := expandToken(tok, expandedCIDRs)
		if err != nil {
			return nil, err
		}
		for _, h := range expanded {
			if seen[h] {
				continue
			}
			seen[h] = true
			hosts = append(hosts, h)
			if len(hosts) > MaxHosts {
				return nil, fmt.Errorf("discovery: at most %d hosts per scan", MaxHosts)
			}
		}
	}
	return hosts, nil
}

// splitTokens splits on commas and any run of whitespace (space, tab, newline, CR), discarding
// empty fields.
func splitTokens(input string) []string {
	return strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || unicode.IsSpace(r)
	})
}

// expandToken parses one token as a CIDR, a single IP, or a hostname, in that order, and returns
// the hosts it stands for. A CIDR whose masked prefix is already in expandedCIDRs returns no hosts:
// every one of them is already in the result, and expanding it again would let many copies of
// one range (which never exceed MaxHosts) cost time and memory per copy.
func expandToken(tok string, expandedCIDRs map[netip.Prefix]bool) ([]string, error) {
	if strings.Contains(tok, "/") {
		prefix, err := netip.ParsePrefix(tok)
		if err != nil {
			return nil, fmt.Errorf("discovery: %q is not a valid CIDR", tok)
		}
		if err := checkCIDRBounds(tok, prefix); err != nil {
			return nil, err
		}
		prefix = prefix.Masked()
		if expandedCIDRs[prefix] {
			return nil, nil
		}
		expandedCIDRs[prefix] = true
		return expandCIDR(prefix), nil
	}

	if addr, err := netip.ParseAddr(tok); err == nil {
		// Canonical text form, not the token verbatim: ParseTargets promises duplicates collapse,
		// which requires one consistent spelling for every address, whichever form the user (or
		// a CIDR expansion elsewhere in the input) wrote it in.
		return []string{addr.String()}, nil
	}

	if !isValidHostname(tok) {
		return nil, fmt.Errorf("discovery: %q is not a valid hostname, IP address or CIDR", tok)
	}
	return []string{tok}, nil
}

// checkCIDRBounds enforces the minimum prefix length and the private-space restriction, naming
// tok (the token exactly as the user typed it) in any error.
func checkCIDRBounds(tok string, prefix netip.Prefix) error {
	minBits := minIPv6Bits
	if prefix.Addr().Is4() {
		minBits = minIPv4Bits
	}
	if prefix.Bits() < minBits {
		return fmt.Errorf("discovery: %q is too large a range (the shortest allowed prefix is /%d)", tok, minBits)
	}
	if !withinPrivateSpace(prefix) {
		return fmt.Errorf("discovery: %q is not within private, loopback, CGNAT or unique-local address space", tok)
	}
	return nil
}

// withinPrivateSpace reports whether prefix lies entirely inside one of privateSpace's ranges:
// the range's prefix must be no more specific than prefix, and prefix's network address must
// fall inside it — together those mean every address prefix covers is also covered by the range.
func withinPrivateSpace(prefix netip.Prefix) bool {
	masked := prefix.Masked()
	for _, p := range privateSpace {
		if p.Bits() <= masked.Bits() && p.Contains(masked.Addr()) {
			return true
		}
	}
	return false
}

// expandCIDR lists every address in prefix, in order, skipping the network and broadcast
// addresses for an IPv4 prefix shorter than /31 (which, like every prefix /31 or /32, and every
// IPv6 prefix, has no such reserved addresses worth excluding).
func expandCIDR(prefix netip.Prefix) []string {
	first := prefix.Masked().Addr()
	last := lastAddr(prefix)
	skipEdges := first.Is4() && prefix.Bits() < 31

	hosts := []string{}
	for addr := first; ; addr = addr.Next() {
		if !(skipEdges && (addr == first || addr == last)) {
			hosts = append(hosts, addr.String())
		}
		if addr == last {
			break
		}
	}
	return hosts
}

// lastAddr returns the highest address in prefix: its network address with every host bit set.
func lastAddr(prefix netip.Prefix) netip.Addr {
	addr := prefix.Masked().Addr()
	b := addr.AsSlice()
	hostBits := addr.BitLen() - prefix.Bits()

	for i := len(b) - 1; hostBits > 0; i-- {
		switch {
		case hostBits >= 8:
			b[i] = 0xff
			hostBits -= 8
		default:
			b[i] |= byte(0xff) >> (8 - hostBits)
			hostBits = 0
		}
	}
	last, _ := netip.AddrFromSlice(b)
	return last
}

// isValidHostname reports whether s is a syntactically valid RFC 1123 hostname: 1-253 characters
// total, made of dot-separated labels of 1-63 characters each, every label alphanumeric with
// optional interior hyphens (never leading or trailing).
func isValidHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !isValidLabel(label) {
			return false
		}
	}
	return true
}

func isValidLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' && i != 0 && i != len(label)-1:
		default:
			return false
		}
	}
	return true
}
