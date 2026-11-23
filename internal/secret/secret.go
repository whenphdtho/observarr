// Package secret encrypts small secrets (API keys) at rest with a key stored in its own file.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// keySize is the AES-256 key length in bytes.
const keySize = 32

// Box encrypts small secrets (API keys) with AES-256-GCM.
type Box struct{ aead cipher.AEAD }

// LoadOrCreate reads the 32-byte key at path, creating it (0600, random) if it does not exist.
// A key file with the wrong size, or that anyone but its owner may write, is an error (never
// silently replaced).
func LoadOrCreate(path string) (*Box, error) {
	key, err := readKey(path)
	if err == nil {
		return newBox(key)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	key, err = createKey(path)
	if err != nil {
		return nil, err
	}
	return newBox(key)
}

// readKey reads and validates an existing key file. It returns an error satisfying
// errors.Is(err, fs.ErrNotExist) if the file does not exist.
func readKey(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("secret: stat key file %s: %w", path, err)
	}
	// Readable by others is accepted (a Docker or Compose secret is mounted 0444); writable or executable is not.
	if perm := info.Mode().Perm(); perm&^0o644 != 0 {
		return nil, fmt.Errorf("secret: key file %s has mode %04o; only its owner may write it", path, perm)
	}
	if info.Size() != keySize {
		return nil, fmt.Errorf("secret: key file %s is %d bytes, want %d; if no services are saved yet, "+
			"delete it to create a new one", path, info.Size(), keySize)
	}

	key := make([]byte, keySize)
	if _, err := io.ReadFull(f, key); err != nil {
		return nil, fmt.Errorf("secret: read key file %s: %w", path, err)
	}
	return key, nil
}

// createKey generates a new random key and installs it at path atomically: it is written in full
// to a temporary file in the same directory, fsynced and closed, then linked into place. A key
// file at path is therefore always either absent or complete — never partially written. If
// another process wins the race and creates path first, createKey reads that key instead. The
// temporary file is always removed.
func createKey(path string) ([]byte, error) {
	dir := filepath.Dir(path)
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secret: generate key: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".key-*")
	if err != nil {
		return nil, fmt.Errorf("secret: create temporary key file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := writeKeyFile(tmp, key); err != nil {
		return nil, err
	}

	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// Lost the race to another concurrent first start. The winner's key file is
			// already complete (it was fsynced before the link that made it visible).
			return readKey(path)
		}
		return nil, fmt.Errorf("secret: link key file %s: %w", path, err)
	}
	if err := fsyncDir(dir); err != nil {
		return nil, fmt.Errorf("secret: sync key directory %s: %w", dir, err)
	}
	return key, nil
}

// writeKeyFile writes key to f with 0600 permissions, fsyncs and closes it.
func writeKeyFile(f *os.File, key []byte) error {
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("secret: chmod temporary key file: %w", err)
	}
	if _, err := f.Write(key); err != nil {
		return fmt.Errorf("secret: write temporary key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("secret: sync temporary key file: %w", err)
	}
	return nil
}

// fsyncDir fsyncs dir so a just-linked directory entry survives a crash.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// newBox builds a Box around key, which must be keySize bytes.
func newBox(key []byte) (*Box, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: create GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext bound to context (e.g. "service:12"), so a ciphertext cannot be moved to another row.
func (b *Box) Seal(plaintext []byte, context string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secret: generate nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte(context)), nil
}

// Open decrypts what Seal produced for the same context; any mismatch or tampering is an error.
func (b *Box) Open(sealed []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("secret: sealed input is too short")
	}
	nonce, ciphertext := sealed[:n], sealed[n:]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, []byte(context))
	if err != nil {
		return nil, errors.New("secret: cannot decrypt (wrong key, wrong context, or the data was tampered with)")
	}
	return plaintext, nil
}
