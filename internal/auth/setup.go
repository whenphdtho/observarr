package auth

import (
	"crypto/rand"
	"encoding/base32"
	"os"
	"path/filepath"
)

// NewSetupToken creates the one-time token that first-start setup asks for and writes it to path (0600). The
// file is written in full under a temporary name, then renamed into place, so it never holds half a token.
func NewSetupToken(path string) (string, error) {
	b := make([]byte, 13) // 104 random bits, of which the 20 characters keep 100
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base32.StdEncoding.EncodeToString(b)[:20]

	f, err := os.CreateTemp(filepath.Dir(path), ".setup-token-*") // created 0600
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name()) // fails harmlessly once renamed
	_, err = f.WriteString(token + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return "", err
	}
	return token, nil
}
