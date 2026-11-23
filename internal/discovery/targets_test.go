package discovery

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseTargetsHostnamesPassThrough(t *testing.T) {
	for _, tok := range []string{"nas.local", "sonarr", "my-host.example.com", "a1-b2.sub.example"} {
		got, err := ParseTargets(tok)
		if err != nil {
			t.Fatalf("ParseTargets(%q): %v", tok, err)
		}
		if len(got) != 1 || got[0] != tok {
			t.Errorf("ParseTargets(%q) = %v, want [%q]", tok, got, tok)
		}
	}
}

func TestParseTargetsInvalidHostnames(t *testing.T) {
	cases := []string{
		"-leading-hyphen",
		"trailing-hyphen-",
		"under_score.local",             // underscore not allowed
		"",                              // handled by splitTokens, but guard the label validator directly too
		"a..b",                          // empty label
		strings.Repeat("a", 64),         // label > 63 chars
		strings.Repeat("a.", 127) + "a", // > 253 chars total
	}
	for _, tok := range cases {
		if tok == "" {
			continue // splitTokens never produces an empty token; isValidHostname alone covers this
		}
		if _, err := ParseTargets(tok); err == nil {
			t.Errorf("ParseTargets(%q): want an error, got none", tok)
		}
	}
}

func TestParseTargetsHostnameErrorNamesToken(t *testing.T) {
	_, err := ParseTargets("-bad-host")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "-bad-host") {
		t.Errorf("error %q does not name the offending token", err)
	}
}

func TestParseTargetsSingleIPsPassThroughAnyAddress(t *testing.T) {
	// Any address passes as-is, including public and link-local: the user named one address
	// they intend to reach, and the HTTP client refuses link-local at dial time, not here.
	for _, tok := range []string{"192.168.1.5", "8.8.8.8", "169.254.1.1", "::1", "2001:db8::1"} {
		got, err := ParseTargets(tok)
		if err != nil {
			t.Fatalf("ParseTargets(%q): %v", tok, err)
		}
		if len(got) != 1 || got[0] != tok {
			t.Errorf("ParseTargets(%q) = %v, want [%q]", tok, got, tok)
		}
	}
}

func TestParseTargetsPrivateIPv4CIDRExpandsSkippingNetworkAndBroadcast(t *testing.T) {
	got, err := ParseTargets("192.168.1.0/24")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	if len(got) != 254 {
		t.Fatalf("len = %d, want 254 (256 minus network and broadcast)", len(got))
	}
	if got[0] != "192.168.1.1" {
		t.Errorf("first host = %q, want 192.168.1.1 (network address skipped)", got[0])
	}
	if got[len(got)-1] != "192.168.1.254" {
		t.Errorf("last host = %q, want 192.168.1.254 (broadcast skipped)", got[len(got)-1])
	}
}

func TestParseTargetsIPv4SlashThirtyOneKeepsBothAddresses(t *testing.T) {
	got, err := ParseTargets("192.168.1.0/31")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	want := []string{"192.168.1.0", "192.168.1.1"}
	if !equalSlices(got, want) {
		t.Errorf("got %v, want %v (no network/broadcast concept at /31)", got, want)
	}
}

func TestParseTargetsIPv4SlashThirtyTwoIsOneHost(t *testing.T) {
	got, err := ParseTargets("192.168.1.5/32")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	if !equalSlices(got, []string{"192.168.1.5"}) {
		t.Errorf("got %v, want [192.168.1.5]", got)
	}
}

func TestParseTargetsCIDRTooLargeIsRejected(t *testing.T) {
	cases := []string{
		"10.0.0.0/8",  // way bigger than /22
		"10.0.0.0/21", // one bit too big
		"fc00::/64",   // bigger than /120
		"fc00::/119",  // one bit too big
	}
	for _, tok := range cases {
		if _, err := ParseTargets(tok); err == nil {
			t.Errorf("ParseTargets(%q): want an error (too large a range), got none", tok)
		}
	}
}

func TestParseTargetsPublicCIDRIsRejected(t *testing.T) {
	for _, tok := range []string{"8.8.8.0/24", "1.1.1.0/24", "2001:db8::/120"} {
		_, err := ParseTargets(tok)
		if err == nil {
			t.Errorf("ParseTargets(%q): want an error (public space), got none", tok)
		}
		if err != nil && !strings.Contains(err.Error(), tok) {
			t.Errorf("error for %q does not name the token: %v", tok, err)
		}
	}
}

func TestParseTargetsAllowedPrivateRanges(t *testing.T) {
	cases := []string{
		"10.0.0.0/22",
		"172.16.0.0/22",
		"192.168.0.0/22",
		"127.0.0.0/22",
		"100.64.0.0/22", // CGNAT
		"fc00::/120",    // ULA
		"::1/128",       // loopback
	}
	for _, tok := range cases {
		if _, err := ParseTargets(tok); err != nil {
			t.Errorf("ParseTargets(%q): %v, want no error", tok, err)
		}
	}
}

func TestParseTargetsIPv6LoopbackSingleHost(t *testing.T) {
	got, err := ParseTargets("::1/128")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	if !equalSlices(got, []string{"::1"}) {
		t.Errorf("got %v, want [::1] (IPv6 never skips edges)", got)
	}
}

func TestParseTargetsInvalidCIDRSyntax(t *testing.T) {
	_, err := ParseTargets("not-a-cidr/24")
	if err == nil {
		t.Fatal("want an error for an unparsable CIDR")
	}
}

func TestParseTargetsDuplicatesRemovedOrderKept(t *testing.T) {
	got, err := ParseTargets("b.local, a.local, b.local")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	if !equalSlices(got, []string{"b.local", "a.local"}) {
		t.Errorf("got %v, want [b.local a.local] (dedup, input order kept)", got)
	}
}

func TestParseTargetsSeparators(t *testing.T) {
	got, err := ParseTargets("a.local, b.local\nc.local\td.local   e.local")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	want := []string{"a.local", "b.local", "c.local", "d.local", "e.local"}
	if !equalSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestParseTargetsEmptyInputIsNonNilEmptySlice(t *testing.T) {
	got, err := ParseTargets("   \n\t  ")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	if got == nil {
		t.Error("got nil, want a non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestParseTargetsMoreThanMaxHostsIsAnError(t *testing.T) {
	// Two disjoint private /22 CIDRs, each yielding 1022 usable hosts (1024 addresses minus
	// network and broadcast): combined they are 2044 unique hosts, over MaxHosts.
	_, err := ParseTargets("10.0.0.0/22 10.0.4.0/22")
	if err == nil {
		t.Fatal("ParseTargets with 2044 unique hosts: want an error, got nil")
	}
	want := "discovery: at most 1024 hosts per scan"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}

func TestParseTargetsBailsOutAsSoonAsMaxHostsIsExceeded(t *testing.T) {
	// 4096 disjoint private /22 CIDRs: fully expanding all of them (over 4 million host
	// strings) before checking the limit would be slow and allocate heavily. ParseTargets must
	// instead stop as soon as the running unique count passes MaxHosts — reached partway through
	// the second token here — so this must return quickly and without allocating anywhere near
	// that much.
	const tokenCount = 4096
	tokens := make([]string, tokenCount)
	for i := 0; i < tokenCount; i++ {
		second := i / 64
		third := (i % 64) * 4 // every 4th value: non-overlapping, prefix-aligned /22s
		tokens[i] = fmt.Sprintf("10.%d.%d.0/22", second, third)
	}
	input := strings.Join(tokens, " ")

	var callErr error
	allocs := testing.AllocsPerRun(1, func() {
		_, callErr = ParseTargets(input)
	})

	if callErr == nil {
		t.Fatal("ParseTargets with 4096 oversized /22 tokens: want an error, got nil")
	}
	want := "discovery: at most 1024 hosts per scan"
	if callErr.Error() != want {
		t.Errorf("error = %q, want %q", callErr, want)
	}
	// Bailing out after ~2 tokens needs on the order of MaxHosts allocations (map entries, slice
	// growth, address strings); fully expanding all 4096 tokens would need orders of magnitude
	// more. This threshold is generous but still far below that, so it only passes if ParseTargets
	// actually stopped early.
	const allocBudget = 50_000
	if allocs > allocBudget {
		t.Errorf("ParseTargets allocated %.0f times for 4096 oversized tokens, want <= %d (did it bail out early?)",
			allocs, allocBudget)
	}
}

func TestParseTargetsRepeatedCIDRIsExpandedOnce(t *testing.T) {
	// 5000 copies of one /22 (1022 hosts, under MaxHosts, so the host limit never fires) — plus
	// the same range written with host bits set — must cost about as much as a single copy.
	input := strings.Repeat("10.0.0.0/22 ", 5000) + "10.0.1.7/22"

	var (
		hosts   []string
		callErr error
	)
	start := time.Now()
	allocs := testing.AllocsPerRun(1, func() {
		hosts, callErr = ParseTargets(input)
	})
	if callErr != nil {
		t.Fatal(callErr)
	}
	if len(hosts) != 1022 {
		t.Fatalf("got %d hosts, want 1022", len(hosts))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("ParseTargets took %v", d)
	}
	// One expansion is ~1022 address strings plus map and slice growth; 5000 expansions would be
	// millions.
	if allocs > 10_000 {
		t.Errorf("ParseTargets allocated %.0f times, want <= 10000 (was the range expanded repeatedly?)", allocs)
	}
}

func TestParseTargetsExactlyMaxHostsIsNotAnError(t *testing.T) {
	hostnames := make([]string, MaxHosts)
	for i := range hostnames {
		hostnames[i] = fmt.Sprintf("h%d.local", i)
	}
	got, err := ParseTargets(strings.Join(hostnames, ","))
	if err != nil {
		t.Fatalf("ParseTargets with exactly %d hosts: %v, want no error", MaxHosts, err)
	}
	if len(got) != MaxHosts {
		t.Fatalf("len = %d, want %d", len(got), MaxHosts)
	}
}

func TestParseTargetsDuplicatesDoNotCountTowardsMaxHosts(t *testing.T) {
	hostnames := make([]string, MaxHosts)
	for i := range hostnames {
		hostnames[i] = fmt.Sprintf("h%d.local", i)
	}
	// Every host repeated twice: 2*MaxHosts tokens, but only MaxHosts unique hosts, so this must
	// not error — the limit applies after dedupe.
	input := strings.Join(hostnames, ",") + "," + strings.Join(hostnames, ",")
	got, err := ParseTargets(input)
	if err != nil {
		t.Fatalf("ParseTargets with %d unique hosts (each repeated): %v, want no error", MaxHosts, err)
	}
	if len(got) != MaxHosts {
		t.Fatalf("len = %d, want %d", len(got), MaxHosts)
	}
}

func TestParseTargetsSingleIPsAreNormalisedForDedupe(t *testing.T) {
	got, err := ParseTargets("2001:DB8::0001, 2001:db8::1")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	if !equalSlices(got, []string{"2001:db8::1"}) {
		t.Errorf("got %v, want [2001:db8::1] (both spellings normalise to the same address)", got)
	}
}

func TestParseTargetsSingleIPDedupesAgainstCIDRMember(t *testing.T) {
	// fc00::/120 expands to fc00::0 .. fc00::ff (256 addresses; IPv6 never skips edges). The
	// leading single-IP token names one of them, in a different case, and must collapse into it
	// rather than appearing twice.
	got, err := ParseTargets("FC00::1, fc00::/120")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	if len(got) != 256 {
		t.Fatalf("len = %d, want 256", len(got))
	}
	count := 0
	for _, h := range got {
		if h == "fc00::1" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("fc00::1 appears %d times, want 1", count)
	}
	// The single-IP token is seen first, so it occupies the first slot; the CIDR's own fc00::1
	// must not have been appended again.
	if got[0] != "fc00::1" {
		t.Errorf("got[0] = %q, want fc00::1", got[0])
	}
}

func TestParseTargetsMixedHostnamesIPsAndCIDRs(t *testing.T) {
	got, err := ParseTargets("nas.local, 192.168.1.5, 192.168.50.0/31")
	if err != nil {
		t.Fatalf("ParseTargets: %v", err)
	}
	want := []string{"nas.local", "192.168.1.5", "192.168.50.0", "192.168.50.1"}
	if !equalSlices(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
