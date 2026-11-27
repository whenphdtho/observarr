package backup

import (
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte(``))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"format":"observarr-settings","version":1}`))
	f.Add([]byte(`{"format":"observarr-settings","version":1,"services":[{"id":1},{"id":1}]}`))
	f.Add([]byte(`{"format":"observarr-settings","version":1,"services":null,"dismissed":[null],"secrets":null}`))
	f.Add([]byte(`{"format":"observarr-settings","version":1,"secrets":{"kdf":{"name":"argon2id","salt":"AAAAAAAAAAAAAAAAAAAAAA==",` +
		`"time":3,"memoryKiB":65536,"threads":4},"cipher":"AES-256-GCM","nonce":"AAAAAAAAAAAAAAAA",` +
		`"data":"AAAAAAAAAAAAAAAAAAAAAA=="}}`))
	f.Add([]byte(`{"format":"observarr-settings","version":1,"secrets":{"kdf":{"time":4294967295,"memoryKiB":4294967295}}}`))
	f.Add([]byte(`{"format":"observarr-settings","version":1}{}`))
	f.Add([]byte(`{"format":"observarr-settings","version":1,"alerts":{"rules":{},"channels":[]}}`))
	f.Add([]byte(`{"format":"observarr-settings","version":2}`))
	f.Add([]byte(`{"format":"observarr-settings","version":2,"services":[{"id":1,"type":"t","name":"n","url":"http://192.0.2.1",` +
		`"hasKey":true}],"alerts":{"rules":{"enabled":{"down":true},"repeatHours":6},"channels":[{"id":1,"type":"ntfy",` +
		`"name":"phone","settings":{"topic":"t"},"rules":["down"],"enabled":true,"secrets":["token"]}]}}`))
	f.Add([]byte(`{"format":"observarr-settings","version":2,"alerts":{"rules":null,"channels":[{"id":7},{"id":7}]}}`))
	f.Add([]byte(`{"format":"observarr-settings","version":2,"alerts":{"channels":[{"settings":[1,2],"rules":null}]},` +
		`"secrets":{"kdf":{"name":"argon2id","salt":"AAAAAAAAAAAAAAAAAAAAAA==","time":3,"memoryKiB":65536,"threads":4},` +
		`"cipher":"AES-256-GCM","nonce":"AAAAAAAAAAAAAAAA","data":"AAAAAAAAAAAAAAAAAAAAAA=="}}`))
	f.Add([]byte(`{"format":"observarr-settings","version":3}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		file, err := Parse(data)
		if err != nil {
			return
		}
		ids := map[int64]bool{}
		for _, s := range file.Services {
			if ids[s.ID] {
				t.Fatalf("service id %d accepted twice", s.ID)
			}
			ids[s.ID] = true
		}
		if file.Version != 1 && file.Version != Version {
			t.Fatalf("accepted version %d", file.Version)
		}
		if a := file.Alerts; a != nil {
			if file.Version == 1 {
				t.Fatal("accepted alerts in a version 1 file")
			}
			chans := map[int64]bool{}
			for _, ch := range a.Channels {
				if chans[ch.ID] {
					t.Fatalf("alert channel id %d accepted twice", ch.ID)
				}
				chans[ch.ID] = true
			}
		}
		if s := file.Secrets; s != nil {
			// Opening the block costs exactly the one key derivation, never what the file asks for.
			if s.KDF.Time != kdfTime || s.KDF.MemoryKiB != kdfMemoryKiB || s.KDF.Threads != kdfThreads {
				t.Fatalf("accepted key derivation %+v", s.KDF)
			}
			if _, err := associated(file); err != nil {
				t.Fatalf("associated data of a parsed file: %v", err)
			}
		}
		if _, err := json.Marshal(file); err != nil {
			t.Fatalf("a parsed file does not encode: %v", err)
		}
	})
}

// FuzzSealOpen seals alert channels' secrets with the rest of a file and reads them back: whatever the channels
// and secrets, the file parses and opens to exactly what was sealed. Each run derives the key twice, so it is slow.
func FuzzSealOpen(f *testing.F) {
	f.Add(int64(1), "ntfy", "Phone", []byte(`{"server":"https://ntfy.sh"}`), "topic", "alerts", "token", "tk_12345678")
	f.Add(int64(-7), "webhook", "Höök  ", []byte(`null`), "url", "https://hook.example/x?t=1", "secret", "\x00\xff")
	f.Fuzz(func(t *testing.T, id int64, typ, name string, settings []byte, k1, v1, k2, v2 string) {
		if !json.Valid(settings) {
			return
		}
		file := File{Format: Format, Version: Version, Services: []Service{}, Dismissed: []string{},
			Alerts: &Alerts{Rules: json.RawMessage(`{}`), Channels: []Channel{{ID: id, Type: typ, Name: name,
				Settings: settings, Secrets: []string{k1, k2}}}}}
		keys := Keys{Services: map[int64]string{}, Channels: map[int64]map[string]string{id: {k1: v1, k2: v2}}}
		if err := Seal(&file, keys, "a passphrase of some length"); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(raw)
		if err != nil {
			t.Fatalf("a sealed file does not parse: %v", err)
		}
		got, err := Open(parsed, "a passphrase of some length")
		if err != nil {
			t.Fatalf("a sealed file does not open: %v", err)
		}
		// Invalid UTF-8 comes back replaced, so compare what the sealed JSON holds, as values:
		// encoders differ in whether they write the replacement character or its escape.
		var want Keys
		if raw, err := json.Marshal(keys); err != nil {
			t.Fatal(err)
		} else if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("opened %+v, sealed %+v", got, want)
		}
		if _, err := Open(parsed, "another passphrase of some length"); err != ErrPassphrase {
			t.Fatalf("a wrong passphrase: %v", err)
		}
	})
}
