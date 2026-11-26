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
		if s := file.Secrets; s != nil {
			// Opening the block costs exactly version 1's key derivation, never what the file asks for.
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
