// Package backup reads and writes the settings file: Observarr's services and dismissed finds in readable JSON,
// with the services' keys in one block encrypted under a passphrase.
//
// The key is derived from the passphrase with argon2id and a random salt; the block is sealed with AES-256-GCM
// under a random nonce. Everything else in the file is the block's associated data, so a file whose readable
// part was changed after export does not open, just as one with a wrong passphrase.
package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	// Format names an Observarr settings file.
	Format = "observarr-settings"
	// Version is the one version of the file this Observarr reads and writes.
	Version = 1
	// MaxFile bounds a settings file.
	MaxFile = 1 << 20
	// MinPassphrase is the shortest passphrase accepted, in characters.
	MinPassphrase = 12
	// MaxPassphrase bounds a passphrase, in bytes.
	MaxPassphrase = 1024

	cipherName = "AES-256-GCM"
	saltLen    = 16
	keyLen     = 32
)

// The key derivation of version 1 (argon2id, RFC 9106's second recommended setting). A file must name exactly
// these, so a crafted file cannot make the server spend more memory or time on it.
const (
	kdfName      = "argon2id"
	kdfTime      = 3
	kdfMemoryKiB = 64 * 1024
	kdfThreads   = 4
)

// ErrPassphrase is returned when the keys do not decrypt: a wrong passphrase, or a file changed after export.
var ErrPassphrase = errors.New("wrong passphrase, or the file was changed after it was exported")

// File is a settings file.
type File struct {
	Format           string    `json:"format"`
	Version          int       `json:"version"`
	CreatedAt        string    `json:"createdAt"`
	ObservarrVersion string    `json:"observarrVersion"`
	Services         []Service `json:"services"`
	Dismissed        []string  `json:"dismissed"` // dismissed discovery finds, "<type> <normalised url>[ <where>]"
	Secrets          *Secrets  `json:"secrets"`   // nil when exported without keys
}

// Service is one service as exported. ID refers to its key in the secrets block; it is not kept on import.
type Service struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Container string `json:"container"`
	HasKey    bool   `json:"hasKey"` // the service had a key; without it in the secrets, it must be entered again
}

// Secrets is the encrypted block: the keys by service ID, as JSON.
type Secrets struct {
	KDF    KDF    `json:"kdf"`
	Cipher string `json:"cipher"`
	Nonce  []byte `json:"nonce"`
	Data   []byte `json:"data"`
}

// KDF names how the block's key is derived from the passphrase.
type KDF struct {
	Name      string `json:"name"`
	Salt      []byte `json:"salt"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memoryKiB"`
	Threads   uint8  `json:"threads"`
}

// CheckPassphrase reports what is wrong with a passphrase for a new file.
func CheckPassphrase(p string) error {
	if len(p) > MaxPassphrase {
		return fmt.Errorf("passphrase: at most %d bytes", MaxPassphrase)
	}
	if utf8.RuneCountInString(p) < MinPassphrase {
		return fmt.Errorf("passphrase: at least %d characters", MinPassphrase)
	}
	return nil
}

// Seal encrypts keys (by service ID) into f.Secrets under passphrase. Call it once f is otherwise complete:
// later changes to f make the block fail to open.
func Seal(f *File, keys map[int64]string, passphrase string) error {
	plain, err := json.Marshal(keys)
	if err != nil {
		return err
	}
	sec := &Secrets{KDF: KDF{Name: kdfName, Salt: make([]byte, saltLen), Time: kdfTime, MemoryKiB: kdfMemoryKiB,
		Threads: kdfThreads}, Cipher: cipherName}
	if _, err := rand.Read(sec.KDF.Salt); err != nil {
		return err
	}
	f.Secrets = sec
	aead, err := newAEAD(sec.KDF, passphrase)
	if err != nil {
		return err
	}
	ad, err := associated(*f)
	if err != nil {
		return err
	}
	sec.Nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(sec.Nonce); err != nil {
		return err
	}
	sec.Data = aead.Seal(nil, sec.Nonce, plain, ad)
	return nil
}

// Open decrypts f's keys with passphrase. It returns ErrPassphrase when they do not decrypt. f must come from
// Parse and hold secrets.
func Open(f File, passphrase string) (map[int64]string, error) {
	aead, err := newAEAD(f.Secrets.KDF, passphrase)
	if err != nil {
		return nil, err
	}
	ad, err := associated(f)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, f.Secrets.Nonce, f.Secrets.Data, ad)
	if err != nil {
		return nil, ErrPassphrase
	}
	var keys map[int64]string
	if err := json.Unmarshal(plain, &keys); err != nil {
		return nil, errors.New("the file's keys are malformed")
	}
	return keys, nil
}

// associated is the data the secrets block is bound to: the whole file but the block's nonce and ciphertext.
func associated(f File) ([]byte, error) {
	sec := *f.Secrets
	sec.Nonce, sec.Data = nil, nil
	f.Secrets = &sec
	return json.Marshal(f)
}

func newAEAD(k KDF, passphrase string) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(passphrase), k.Salt, k.Time, k.MemoryKiB, k.Threads, keyLen)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Parse reads a settings file strictly: the format and version must be known, every field known, the key
// derivation exactly version 1's, and every service ID unique. It does not check services or dismissed finds
// against the server's apps; that is the importer's work.
func Parse(data []byte) (File, error) {
	if len(data) > MaxFile {
		return File{}, fmt.Errorf("the file is larger than %d MiB", MaxFile>>20)
	}
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return File{}, errors.New("not an Observarr settings file (it is not valid JSON)")
	}
	if head.Format != Format {
		return File{}, errors.New("not an Observarr settings file")
	}
	if head.Version != Version {
		return File{}, fmt.Errorf("settings file version %d is not supported; this Observarr reads version %d",
			head.Version, Version)
	}

	var f File
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil { // data after the value was refused by the first decoding already
		return File{}, fmt.Errorf("malformed settings file: %v", err)
	}
	ids := make(map[int64]bool, len(f.Services))
	for _, svc := range f.Services {
		if ids[svc.ID] {
			return File{}, fmt.Errorf("malformed settings file: service id %d appears twice", svc.ID)
		}
		ids[svc.ID] = true
	}
	if s := f.Secrets; s != nil {
		k := s.KDF
		if k.Name != kdfName || k.Time != kdfTime || k.MemoryKiB != kdfMemoryKiB || k.Threads != kdfThreads ||
			len(k.Salt) != saltLen || s.Cipher != cipherName || len(s.Nonce) != 12 || len(s.Data) < 16 {
			return File{}, errors.New("malformed settings file: unknown encryption of the keys")
		}
	}
	return f, nil
}
