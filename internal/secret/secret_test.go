package secret

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestBox(t *testing.T) (*Box, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret.key")
	b, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	return b, path
}

func TestSealOpenRoundTrip(t *testing.T) {
	b, _ := newTestBox(t)
	plaintext := []byte("super-secret-api-key")
	sealed, err := b.Seal(plaintext, "service:1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := b.Open(sealed, "service:1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("Open: got %q, want %q", got, plaintext)
	}
}

func TestOpenWrongContextFails(t *testing.T) {
	b, _ := newTestBox(t)
	sealed, err := b.Seal([]byte("secret"), "service:1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := b.Open(sealed, "service:2"); err == nil {
		t.Error("Open with wrong context: want error, got nil")
	}
}

func TestOpenFlippedByteFails(t *testing.T) {
	b, _ := newTestBox(t)
	sealed, err := b.Seal([]byte("secret"), "service:1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := b.Open(tampered, "service:1"); err == nil {
		t.Error("Open tampered ciphertext: want error, got nil")
	}
}

func TestSealTwiceDiffers(t *testing.T) {
	b, _ := newTestBox(t)
	a, err := b.Seal([]byte("secret"), "service:1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	c, err := b.Seal([]byte("secret"), "service:1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Equal(a, c) {
		t.Error("two Seals of the same plaintext/context produced identical output")
	}
}

func TestLoadOrCreateTwiceInteroperates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	b1, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("first LoadOrCreate: %v", err)
	}
	b2, err := LoadOrCreate(path)
	if err != nil {
		t.Fatalf("second LoadOrCreate: %v", err)
	}

	sealed1, err := b1.Seal([]byte("from-b1"), "ctx")
	if err != nil {
		t.Fatalf("Seal (b1): %v", err)
	}
	got, err := b2.Open(sealed1, "ctx")
	if err != nil {
		t.Fatalf("Open (b2) of b1's output: %v", err)
	}
	if string(got) != "from-b1" {
		t.Errorf("got %q, want %q", got, "from-b1")
	}

	sealed2, err := b2.Seal([]byte("from-b2"), "ctx")
	if err != nil {
		t.Fatalf("Seal (b2): %v", err)
	}
	got, err = b1.Open(sealed2, "ctx")
	if err != nil {
		t.Fatalf("Open (b1) of b2's output: %v", err)
	}
	if string(got) != "from-b2" {
		t.Errorf("got %q, want %q", got, "from-b2")
	}
}

func TestKeyFileMode0600(t *testing.T) {
	_, path := newTestBox(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode: got %04o, want 0600", perm)
	}
}

func TestLoadOrCreate31ByteKeyFileErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(path, make([]byte, 31), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadOrCreate(path); err == nil {
		t.Error("LoadOrCreate with 31-byte key file: want error, got nil")
	}
}

func TestLoadOrCreateWritableKeyFileErrorsAndFileUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	want := bytes.Repeat([]byte{0x42}, 32)
	if err := os.WriteFile(path, want, 0o666); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil { // past the umask
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err == nil {
		t.Error("LoadOrCreate with 0666 key file: want error, got nil")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("key file content changed after a rejected LoadOrCreate")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o666 {
		t.Error("key file mode changed after a rejected LoadOrCreate")
	}
}

// A key file handed in read-only, like a Docker or Compose secret (0444), is fine; one that others can change is not.
func TestLoadOrCreateKeyFileModes(t *testing.T) {
	for mode, ok := range map[os.FileMode]bool{
		0o400: true, 0o600: true, 0o640: true, 0o444: true, 0o644: true,
		0o620: false, 0o602: false, 0o700: false, 0o655: false,
	} {
		path := filepath.Join(t.TempDir(), "secret.key")
		if err := os.WriteFile(path, bytes.Repeat([]byte{0x7a}, keySize), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreate(path); (err == nil) != ok {
			t.Errorf("key file mode %04o: err = %v, want accepted %v", mode, err, ok)
		}
	}
}

func TestLoadOrCreateConcurrentProducesExactlyOneKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")

	const n = 10
	boxes := make([]*Box, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			boxes[i], errs[i] = LoadOrCreate(path)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("LoadOrCreate[%d]: %v", i, err)
		}
	}

	// Every box must interoperate: whatever one seals, any other can open.
	sealed, err := boxes[0].Seal([]byte("payload"), "ctx")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	for i, b := range boxes {
		got, err := b.Open(sealed, "ctx")
		if err != nil {
			t.Fatalf("Open with box[%d]: %v", i, err)
		}
		if string(got) != "payload" {
			t.Errorf("box[%d]: got %q, want %q", i, got, "payload")
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("directory contains %v, want exactly [%s]", names, filepath.Base(path))
	}
}

func TestOpenTruncatedSealedInputErrors(t *testing.T) {
	b, _ := newTestBox(t)
	if _, err := b.Open([]byte("short"), "service:1"); err == nil {
		t.Error("Open with truncated sealed input: want error, got nil")
	}
	if _, err := b.Open(nil, "service:1"); err == nil {
		t.Error("Open with empty sealed input: want error, got nil")
	}
}

func TestSealErrorsNeverContainKeyOrPlaintext(t *testing.T) {
	b, _ := newTestBox(t)
	secretValue := "do-not-leak-me-12345"
	sealed, err := b.Seal([]byte(secretValue), "service:1")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if _, err := b.Open(sealed, "wrong-context"); err != nil {
		if bytes.Contains([]byte(err.Error()), []byte(secretValue)) {
			t.Errorf("Open error leaked plaintext: %v", err)
		}
	}
}
