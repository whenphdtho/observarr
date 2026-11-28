package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const pass = "correct horse battery"

func sealed(t *testing.T) []byte {
	t.Helper()
	f := File{Format: Format, Version: Version, CreatedAt: "2026-11-25T10:00:00Z", ObservarrVersion: "test",
		Services: []Service{
			{ID: 3, Type: "fake", Name: "Fake", URL: "http://fake:8989", HasKey: true},
			{ID: 7, Type: "keyless", Name: "Open", URL: "http://open:80", Container: "open"},
		},
		Dismissed: []string{"fake http://other:8989"},
		Alerts: &Alerts{Rules: json.RawMessage(`{"perHour":30}`), Channels: []Channel{
			{ID: 4, Type: "ntfy", Name: "Phone", Settings: json.RawMessage(`{"topic":"t"}`), Rules: []string{"down"},
				Enabled: true, Secrets: []string{"token"}}}}}
	keys := Keys{Services: map[int64]string{3: "secret-key-3"}, Channels: map[int64]map[string]string{4: {"token": "tk_secret"}}}
	if err := Seal(&f, keys, pass); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("secret-key-3")) || bytes.Contains(data, []byte("tk_secret")) ||
		bytes.Contains(data, []byte(pass)) {
		t.Fatal("the file holds a key or the passphrase in plain text")
	}
	return data
}

func TestRoundTrip(t *testing.T) {
	f, err := Parse(sealed(t))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := Open(f, pass)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys.Services) != 1 || keys.Services[3] != "secret-key-3" || keys.Channels[4]["token"] != "tk_secret" {
		t.Fatalf("keys = %v", keys)
	}
	if len(f.Services) != 2 || f.Services[1].Container != "open" || f.Dismissed[0] != "fake http://other:8989" ||
		f.Alerts.Channels[0].Name != "Phone" || string(f.Alerts.Rules) != `{"perHour":30}` {
		t.Fatalf("file = %+v", f)
	}
}

// A version 1 file, written before alerts existed, still opens: its block holds the services' keys alone.
func TestReadsVersion1(t *testing.T) {
	f := File{Format: Format, Version: 1, Services: []Service{{ID: 3, Type: "fake", Name: "F", URL: "http://f", HasKey: true}}}
	if err := Seal(&f, Keys{}, pass); err != nil {
		t.Fatal(err)
	}
	// Seal writes the current block; build version 1's by hand: the services' keys as a map by ID.
	sec := f.Secrets
	aead, err := newAEAD(sec.KDF, pass)
	if err != nil {
		t.Fatal(err)
	}
	ad, _ := associated(f)
	sec.Data = aead.Seal(nil, sec.Nonce, []byte(`{"3":"secret-key-3"}`), ad)
	data, _ := json.Marshal(f)
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := Open(got, pass)
	if err != nil || keys.Services[3] != "secret-key-3" || keys.Channels != nil || got.Alerts != nil {
		t.Fatalf("Open = %+v, %v", keys, err)
	}
	withAlerts := strings.Replace(string(data), `"secrets"`, `"alerts":{"rules":{},"channels":[]},"secrets"`, 1)
	if _, err := Parse([]byte(withAlerts)); err == nil || !strings.Contains(err.Error(), "alerts in a version 1 file") {
		t.Fatalf("version 1 with alerts: Parse = %v", err)
	}
}

func TestWrongPassphrase(t *testing.T) {
	f, err := Parse(sealed(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(f, pass+"!"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("Open with a wrong passphrase = %v", err)
	}
}

// Any change to the readable part, the header included, or to the ciphertext makes the keys fail to open.
func TestTamperedFile(t *testing.T) {
	data := sealed(t)
	edits := map[string]func(*File){
		"created at":        func(f *File) { f.CreatedAt = "2020-01-01T00:00:00Z" },
		"observarr version": func(f *File) { f.ObservarrVersion = "other" },
		"service url":       func(f *File) { f.Services[0].URL = "http://elsewhere:8989" },
		"service type":      func(f *File) { f.Services[0].Type = "keyless" },
		"service dropped":   func(f *File) { f.Services = f.Services[:1] },
		"dismissed":         func(f *File) { f.Dismissed = nil },
		"channel settings":  func(f *File) { f.Alerts.Channels[0].Settings = json.RawMessage(`{"topic":"u"}`) },
		"alert rules":       func(f *File) { f.Alerts.Rules = json.RawMessage(`{"perHour":600}`) },
		"salt":              func(f *File) { f.Secrets.KDF.Salt[0] ^= 1 },
		"nonce":             func(f *File) { f.Secrets.Nonce[0] ^= 1 },
		"ciphertext":        func(f *File) { f.Secrets.Data[len(f.Secrets.Data)/2] ^= 1 },
	}
	for name, edit := range edits {
		f, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		edit(&f)
		if _, err := Open(f, pass); !errors.Is(err, ErrPassphrase) {
			t.Errorf("%s changed: Open = %v, want ErrPassphrase", name, err)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	good := string(sealed(t))
	cases := map[string]struct{ data, want string }{
		"not json":          {"nope", "not valid JSON"},
		"other format":      {`{"format":"other","version":2}`, "not an Observarr settings file"},
		"newer version":     {strings.Replace(good, `"version":2`, `"version":3`, 1), "version 3 is not supported"},
		"unknown field":     {strings.Replace(good, `"version":2`, `"version":2,"extra":true`, 1), "malformed"},
		"duplicate channel": {strings.Replace(good, `"channels":[`, `"channels":[{"id":4},`, 1), "appears twice"},
		"trailing data":     {good + "{}", "not valid JSON"},
		"duplicate id":      {strings.Replace(good, `"id":7`, `"id":3`, 1), "appears twice"},
		"weaker kdf":        {strings.Replace(good, `"time":3`, `"time":1`, 1), "unknown encryption"},
		"costlier kdf":      {strings.Replace(good, `"memoryKiB":65536`, `"memoryKiB":4194304`, 1), "unknown encryption"},
		"other cipher":      {strings.Replace(good, `"AES-256-GCM"`, `"none"`, 1), "unknown encryption"},
		"too large":         {good[:len(good)-1] + `,"pad":"` + strings.Repeat("x", MaxFile) + `"}`, "larger than 1 MiB"},
		"missing format":    {`{"version":1}`, "not an Observarr settings file"},
	}
	for name, c := range cases {
		if _, err := Parse([]byte(c.data)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: Parse = %v, want %q", name, err, c.want)
		}
	}
}

func TestWithoutKeys(t *testing.T) {
	f := File{Format: Format, Version: Version, Services: []Service{{ID: 1, Type: "fake", Name: "F", URL: "http://f", HasKey: true}}}
	data, _ := json.Marshal(f)
	got, err := Parse(data)
	if err != nil || got.Secrets != nil || !got.Services[0].HasKey {
		t.Fatalf("Parse = %+v, %v", got, err)
	}
}

func TestCheckPassphrase(t *testing.T) {
	if CheckPassphrase("short") == nil || CheckPassphrase(strings.Repeat("ä", 11)) == nil ||
		CheckPassphrase(strings.Repeat("x", MaxPassphrase+1)) == nil {
		t.Error("a passphrase too short or too long was accepted")
	}
	if err := CheckPassphrase(strings.Repeat("ä", 12)); err != nil {
		t.Errorf("12 characters refused: %v", err)
	}
}
