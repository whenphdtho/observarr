// Package auth handles passwords, sessions, login throttling and trusted-proxy identity.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (OWASP baseline: 19 MiB, 2 passes, 1 lane).
const (
	argonMemoryKiB = 19 * 1024
	argonPasses    = 2
	argonLanes     = 1
	argonKeyLen    = 32
	saltLen        = 16
	maxMemoryKiB   = 256 * 1024 // refuse stored hashes that would make a login exhaust memory
	maxPasses      = 10
)

var b64 = base64.RawStdEncoding

// HashPassword returns an argon2id hash in the standard PHC string format.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonPasses, argonMemoryKiB, argonLanes, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonPasses, argonLanes, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches the encoded hash. Unusable ("!") and malformed hashes,
// and hashes with unreasonable cost parameters, never match.
func CheckPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version ||
		parts[2] != fmt.Sprintf("v=%d", version) { // reject trailing garbage Sscanf would silently ignore
		return false
	}
	var memory, passes uint32
	var lanes uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &passes, &lanes); err != nil ||
		memory == 0 || memory > maxMemoryKiB || passes == 0 || passes > maxPasses || lanes == 0 ||
		parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, passes, lanes) { // same, for the cost parameters
		return false
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, passes, memory, lanes, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyHash returns a valid hash of a random password. Checking a login against it for unknown users makes
// "no such user" take as long as "wrong password", so user names cannot be probed by timing.
var DummyHash = sync.OnceValue(func() string {
	h, err := HashPassword(rand.Text())
	if err != nil {
		panic(err) // crypto/rand failing is unrecoverable
	}
	return h
})
