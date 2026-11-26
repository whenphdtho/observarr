package backup

import (
	"encoding/json"
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
