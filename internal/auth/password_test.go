package auth

import (
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword("correct horse battery staple", h) {
		t.Fatal("correct password rejected")
	}
	if CheckPassword("wrong password", h) {
		t.Fatal("wrong password accepted")
	}
}

func TestHashesAreSalted(t *testing.T) {
	a, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same password")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two hashes of the same password must differ (random salt)")
	}
}

func TestUnusableAndMalformedHashesNeverMatch(t *testing.T) {
	for _, h := range []string{
		"!", "", "$argon2id$v=19$m=19456,t=2,p=1$bad", "$bcrypt$x$y$z$w",
		"$argon2id$v=19$m=99999999,t=2,p=1$c2FsdA$aGFzaA", // absurd memory cost is refused
		"$argon2id$v=19$m=19456,t=2,p=0$c2FsdA$aGFzaA",    // zero lanes
		"$argon2id$v=19$m=19456,t=2,p=1$$aGFzaA",          // empty salt
	} {
		if CheckPassword("anything", h) {
			t.Errorf("hash %q matched", h)
		}
	}
}

// TestStrictPHCParsingRejectsTamperedFields proves parsing is strict, not merely lenient-and-lucky: it
// tampers with a real, otherwise-matching hash so that a loose Sscanf would still accept it (ignoring
// trailing bytes after the fields it scans), and checks that it is rejected anyway.
func TestStrictPHCParsingRejectsTamperedFields(t *testing.T) {
	h, err := HashPassword("anything")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword("anything", h) {
		t.Fatal("sanity check: the untampered hash must match")
	}
	parts := strings.Split(h, "$")
	if len(parts) != 6 {
		t.Fatalf("hash %q has %d parts, want 6", h, len(parts))
	}

	tampered := func(field int, value string) string {
		p := append([]string(nil), parts...)
		p[field] = value
		return strings.Join(p, "$")
	}

	if h := tampered(2, parts[2]+"x"); CheckPassword("anything", h) {
		t.Error("trailing garbage after the version must be rejected")
	}
	if h := tampered(3, parts[3]+"x"); CheckPassword("anything", h) {
		t.Error("trailing garbage after the cost parameters must be rejected")
	}
	if h := tampered(4, ""); CheckPassword("anything", h) {
		t.Error("an empty salt must be rejected")
	}
}
