package auth

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestNewSetupToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup-token")
	token, err := NewSetupToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Z2-7]{20}$`).MatchString(token) {
		t.Fatalf("token %q is not 20 base32 characters", token)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %04o, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(path); string(b) != token+"\n" {
		t.Fatalf("token file holds %q", b)
	}
	again, err := NewSetupToken(path) // a restart before setup replaces the token
	if err != nil || again == token {
		t.Fatalf("second token = %q, %v", again, err)
	}
	if b, _ := os.ReadFile(path); string(b) != again+"\n" {
		t.Fatalf("token file holds %q, want the new token", b)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*")); len(left) != 1 {
		t.Fatalf("files left behind: %v", left)
	}
}
