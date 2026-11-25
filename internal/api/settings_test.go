package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"observarr/internal/auth"
	"observarr/internal/backup"
	"observarr/internal/shape"
)

const (
	passphrase   = "a passphrase of some length"
	userPassword = "a long enough password" // the test admin's (see admin)
	// withKeys asks for an export with the keys: the file's passphrase and the account's password.
	withKeys = `{"passphrase":"` + passphrase + `","password":"` + userPassword + `"}`
)

// exported adds a fake service with testKey ("Alpha") and a keyless one ("Open"), dismisses a find, and returns
// the settings file exported with body.
func (e *servicesEnv) exported(body string) []byte {
	e.t.Helper()
	e.create("Alpha")
	if code, data := e.send("POST", "/api/services",
		fmt.Sprintf(`{"type":"keyless","name":"Open","url":"%s/open","container":"open"}`, e.appURL)); code != 201 {
		e.t.Fatalf("create Open = %d %v", code, data)
	}
	if err := e.srv.Store.Dismiss(context.Background(), "fake http://192.0.2.7:8989"); err != nil {
		e.t.Fatal(err)
	}
	code, raw, h := e.raw("POST", "/api/settings/export", body, map[string]string{"X-CSRF-Token": e.csrf})
	if code != 200 {
		e.t.Fatalf("export = %d %s", code, raw)
	}
	if cd := h.Get("Content-Disposition"); !strings.HasPrefix(cd, `attachment; filename="observarr-settings-`) {
		e.t.Fatalf("Content-Disposition = %q", cd)
	}
	return raw
}

// importFile sends file to /api/settings/import and decodes the answer.
func (e *servicesEnv) importFile(file []byte, pass, mode string) (int, importReport, string) {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"file": string(file), "passphrase": pass, "mode": mode})
	code, raw, _ := e.raw("POST", "/api/settings/import", string(body), map[string]string{"X-CSRF-Token": e.csrf})
	var rep importReport
	var fail struct{ Error string }
	if code == 200 {
		if err := json.Unmarshal(raw, &rep); err != nil {
			e.t.Fatalf("import: %s", raw)
		}
	} else {
		json.Unmarshal(raw, &fail)
	}
	return code, rep, fail.Error
}

// saved returns the names of the saved services.
func (e *servicesEnv) saved() []string {
	e.t.Helper()
	svcs, err := e.srv.Store.Services(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for _, s := range svcs {
		names = append(names, s.Name)
	}
	return names
}

func TestSettingsRoundTrip(t *testing.T) {
	src := newServicesEnv(t)
	file := src.exported(withKeys)
	if !src.hasAudit("exported settings: services: 2, keys encrypted: 1") {
		t.Fatalf("audit = %v", src.auditRows())
	}

	dst := newServicesEnv(t) // a fresh store with a key box of its own
	code, rep, msg := dst.importFile(file, passphrase, "preview")
	if code != 200 || rep.Applied || len(rep.Services) != 2 || rep.Dismissed != 1 {
		t.Fatalf("preview = %d %+v %s", code, rep, msg)
	}
	if rep.Services[0].Name != "Alpha" || rep.Services[0].Key != "included" || rep.Services[1].Key != "none" ||
		rep.Services[1].Container != "open" || rep.Services[1].URL != src.appURL+"/open" {
		t.Fatalf("preview services = %+v", rep.Services)
	}
	if len(dst.saved()) != 0 || len(dst.srv.Hub.Snapshot()) != 0 {
		t.Fatal("a preview saved something")
	}

	code, rep, msg = dst.importFile(file, passphrase, "add")
	if code != 200 || !rep.Applied || len(rep.Services) != 2 || len(rep.Skipped) != 0 {
		t.Fatalf("import = %d %+v %s", code, rep, msg)
	}
	svcs, _ := dst.srv.Store.Services(context.Background())
	if len(svcs) != 2 {
		t.Fatalf("saved = %+v", svcs)
	}
	if key, err := dst.srv.openKey(svcs[0]); err != nil || key != testKey {
		t.Fatalf("imported key = %v (sealed with this server's box: %v)", key == testKey, err)
	}
	if svcs[1].SealedKey != nil || svcs[1].Container != "open" {
		t.Fatalf("keyless service = %+v", svcs[1])
	}
	if d, _ := dst.srv.Store.Dismissed(context.Background()); !d["fake http://192.0.2.7:8989"] {
		t.Fatal("dismissed find not imported")
	}
	// Imported services are polled like saved ones, with the key; nothing checked them on import.
	eventually(t, "the imported service is polled", func() bool { k, _ := dst.app.lastProbe(); return k == testKey })
	if dst.app.detects.Load() != 0 {
		t.Fatal("import contacted the app")
	}
	if !dst.hasAudit("imported settings: added: 2, skipped: 0, keys decrypted") {
		t.Fatalf("audit = %v", dst.auditRows())
	}

	// Adding the same file again skips the names that are taken.
	code, rep, _ = dst.importFile(file, passphrase, "add")
	if code != 200 || len(rep.Services) != 0 || len(rep.Skipped) != 2 || len(dst.saved()) != 2 {
		t.Fatalf("second import = %d %+v", code, rep)
	}
}

func TestSettingsWrongPassphraseImportsNothing(t *testing.T) {
	src := newServicesEnv(t)
	file := src.exported(withKeys)
	dst := newServicesEnv(t)
	for _, mode := range []string{"preview", "add"} {
		if code, _, msg := dst.importFile(file, passphrase+"?", mode); code != 422 || !strings.Contains(msg, "wrong passphrase") {
			t.Fatalf("%s with a wrong passphrase = %d %s", mode, code, msg)
		}
	}
	if len(dst.saved()) != 0 {
		t.Fatal("a wrong passphrase imported services")
	}
	if !dst.hasAudit("settings import failed: 2 wrong passphrases from 127.0.0.1 in 15 min") {
		t.Fatalf("audit = %v", dst.auditRows())
	}
	if code, _, msg := dst.importFile(file, "", "add"); code != 400 || !strings.Contains(msg, "enter its passphrase") {
		t.Fatalf("no passphrase = %d %s", code, msg)
	}
	// Without keys, on request: the services come, their keys are to be entered again.
	body, _ := json.Marshal(map[string]any{"file": string(file), "withoutKeys": true, "mode": "add"})
	if code, _, _ := dst.raw("POST", "/api/settings/import", string(body), map[string]string{"X-CSRF-Token": dst.csrf}); code != 200 {
		t.Fatalf("import without keys = %d", code)
	}
	if got := dst.saved(); len(got) != 2 {
		t.Fatalf("saved = %v", got)
	}
}

func TestSettingsWrongPassphrasesAreLimited(t *testing.T) {
	src := newServicesEnv(t)
	file := src.exported(withKeys)
	for range auth.MaxAttempts {
		src.importFile(file, "not the passphrase", "preview")
	}
	if code, _, msg := src.importFile(file, passphrase, "preview"); code != 429 || !strings.Contains(msg, "too many wrong passphrases") {
		t.Fatalf("after %d wrong passphrases = %d %s", auth.MaxAttempts, code, msg)
	}
}

func TestSettingsTamperedFileIsRefused(t *testing.T) {
	src := newServicesEnv(t)
	file := src.exported(withKeys)
	var f backup.File
	if err := json.Unmarshal(file, &f); err != nil {
		t.Fatal(err)
	}
	dst := newServicesEnv(t)
	for name, edit := range map[string]func(*backup.File){
		"header":     func(f *backup.File) { f.CreatedAt = "2020-01-01T00:00:00Z" },
		"url":        func(f *backup.File) { f.Services[0].URL = "http://198.51.100.9:8989" },
		"ciphertext": func(f *backup.File) { f.Secrets.Data[0] ^= 1 },
	} {
		g := f
		g.Services = slices.Clone(f.Services)
		sec := *f.Secrets
		sec.Data = slices.Clone(sec.Data)
		g.Secrets = &sec
		edit(&g)
		changed, _ := json.Marshal(g)
		if code, _, msg := dst.importFile(changed, passphrase, "add"); code != 422 {
			t.Errorf("%s changed: import = %d %s", name, code, msg)
		}
	}
	if len(dst.saved()) != 0 {
		t.Fatal("a tampered file imported services")
	}
}

func TestSettingsExportWithoutKeys(t *testing.T) {
	src := newServicesEnv(t)
	file := src.exported(`{"withoutKeys":true}`)
	var f backup.File
	if err := json.Unmarshal(file, &f); err != nil || f.Secrets != nil || len(f.Services) != 2 || !f.Services[0].HasKey {
		t.Fatalf("file = %s", file)
	}
	if !src.hasAudit("exported settings: services: 2, without keys") {
		t.Fatalf("audit = %v", src.auditRows())
	}

	dst := newServicesEnv(t)
	code, rep, msg := dst.importFile(file, "", "add")
	if code != 200 || rep.Services[0].Key != "missing" {
		t.Fatalf("import = %d %+v %s", code, rep, msg)
	}
	svcs, _ := dst.srv.Store.Services(context.Background())
	if svcs[0].SealedKey != nil {
		t.Fatal("a key appeared from nowhere")
	}
	if r := dst.serviceState(svcs[0].ID).Readings[shape.KindHealth]; r.Error != errKeyMissing.Error() {
		t.Fatalf("health of a service imported without its key = %+v", r)
	}
}

func TestSettingsExportRefusesAShortPassphrase(t *testing.T) {
	e := newServicesEnv(t)
	for _, body := range []string{`{"passphrase":"short"}`, `{}`, `{"passphrase":"` + passphrase + `","withoutKeys":true}`} {
		if code, _ := e.send("POST", "/api/settings/export", body); code != 400 {
			t.Errorf("export %s = %d, want 400", body, code)
		}
	}
}

func TestSettingsImportValidates(t *testing.T) {
	e := newServicesEnv(t)
	plain := func(services string) []byte {
		return []byte(`{"format":"observarr-settings","version":1,"createdAt":"","observarrVersion":"",` +
			`"services":[` + services + `],"dismissed":[],"secrets":null}`)
	}
	svc := func(id int, typ, name, url string) string {
		return fmt.Sprintf(`{"id":%d,"type":%q,"name":%q,"url":%q,"container":"","hasKey":false}`, id, typ, name, url)
	}
	ok := svc(1, "keyless", "Open", e.appURL+"/open")

	cases := []struct {
		name string
		file []byte
		code int
		msg  string
	}{
		{"too large", append(plain(ok), strings.Repeat(" ", backup.MaxFile)...), 413, "larger than 1 MiB"},
		{"unknown version", []byte(`{"format":"observarr-settings","version":9}`), 400, "version 9 is not supported"},
		{"not a settings file", []byte(`{"services":[]}`), 400, "not an Observarr settings file"},
		{"bad url", plain(ok + "," + svc(2, "keyless", "Bad", "ftp://x")), 400, `service "Bad": url:`},
		{"bad container", plain(strings.Replace(ok, `"container":""`, `"container":"-x"`, 1)), 400, "container:"},
		{"twice", plain(ok + "," + svc(2, "keyless", "open", e.appURL)), 400, "names two services"},
	}
	for _, c := range cases {
		if code, _, msg := e.importFile(c.file, "", "add"); code != c.code || !strings.Contains(msg, c.msg) {
			t.Errorf("%s: import = %d %q, want %d %q", c.name, code, msg, c.code, c.msg)
		}
	}
	if len(e.saved()) != 0 {
		t.Fatal("an invalid file imported services")
	}

	// A body beyond any file's size is refused before it is read in full.
	huge := `{"file":"` + strings.Repeat("x", maxImportBody) + `","mode":"add"}`
	if code, _ := e.send("POST", "/api/settings/import", huge); code != 413 {
		t.Errorf("huge body = %d, want 413", code)
	}

	// An unknown app type is skipped and reported, the rest imported.
	code, rep, msg := e.importFile(plain(ok+","+svc(2, "future", "Later", "http://later:1")), "", "add")
	if code != 200 || len(rep.Services) != 1 || len(rep.Skipped) != 1 || rep.Skipped[0].Reason != "unknown app type" {
		t.Fatalf("unknown type = %d %+v %s", code, rep, msg)
	}
	if got := e.saved(); len(got) != 1 || got[0] != "Open" {
		t.Fatalf("saved = %v", got)
	}
}

// A replacing import that fails midway changes nothing: the saved services stay, and keep being polled.
func TestSettingsReplaceIsOneTransaction(t *testing.T) {
	src := newServicesEnv(t)
	file := src.exported(withKeys)
	dst := newServicesEnv(t)
	dst.create("Keep")
	db, err := sql.Open("sqlite", filepath.Join(dst.dir, "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// The second service of the file fails to insert.
	if _, err := db.Exec(`CREATE TRIGGER boom BEFORE INSERT ON services WHEN NEW.name = 'Open'
		BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := dst.importFile(file, passphrase, "replace"); code != 500 {
		t.Fatalf("failing import = %d, want 500", code)
	}
	if got := dst.saved(); len(got) != 1 || got[0] != "Keep" || len(dst.srv.Hub.Snapshot()) != 1 {
		t.Fatalf("after a failed import: saved %v, hub %d", got, len(dst.srv.Hub.Snapshot()))
	}

	if _, err := db.Exec("DROP TRIGGER boom"); err != nil {
		t.Fatal(err)
	}
	code, rep, msg := dst.importFile(file, passphrase, "replace")
	if code != 200 || rep.Removed != 1 || len(rep.Services) != 2 {
		t.Fatalf("replace = %d %+v %s", code, rep, msg)
	}
	hub := dst.srv.Hub.Snapshot()
	if got := dst.saved(); len(got) != 2 || got[0] != "Alpha" || len(hub) != 2 {
		t.Fatalf("after replacing: saved %v, hub %+v", got, hub)
	}
	if !dst.hasAudit("imported settings: added: 2, skipped: 0, removed: 1, keys decrypted") {
		t.Fatalf("audit = %v", dst.auditRows())
	}
}

func TestSettingsNeedSessionAndCSRF(t *testing.T) {
	e := newServicesEnv(t)
	anon := newClientFor(e.client)
	for _, path := range []string{"/api/settings/export", "/api/settings/import"} {
		body := `{"withoutKeys":true}`
		if path == "/api/settings/import" {
			body = `{"file":"{}","mode":"preview"}`
		}
		if code, _, _ := anon.raw("POST", path, body, nil); code != 401 {
			t.Errorf("%s without a session = %d, want 401", path, code)
		}
		if code, _, _ := e.raw("POST", path, body, nil); code != 403 {
			t.Errorf("%s without CSRF = %d, want 403", path, code)
		}
		if code, _, _ := e.raw("GET", path, "", nil); code != 404 {
			t.Errorf("GET %s = %d, want 404", path, code)
		}
	}
	for _, row := range e.auditRows() {
		if strings.Contains(row, "settings") {
			t.Fatalf("a refused request was audited: %s", row)
		}
	}
}

// export sends an export request with body and returns the status and the answer.
func (e *servicesEnv) export(body string) (int, string) {
	e.t.Helper()
	code, raw, _ := e.raw("POST", "/api/settings/export", body, map[string]string{"X-CSRF-Token": e.csrf})
	return code, string(raw)
}

func TestSettingsExportWithKeysNeedsThePassword(t *testing.T) {
	e := newServicesEnv(t)
	e.create("Alpha")
	wrong := `{"passphrase":"` + passphrase + `","password":"not the password"}`
	for _, body := range []string{
		`{"passphrase":"` + passphrase + `"}`,
		`{"withoutKeys":true,"password":"` + userPassword + `"}`,
	} {
		if code, raw := e.export(body); code != 400 {
			t.Errorf("export %s = %d %s, want 400", body, code, raw)
		}
	}
	if code, raw := e.export(wrong); code != 403 || strings.Contains(raw, "secrets") {
		t.Fatalf("export with a wrong password = %d %s", code, raw)
	}
	if !e.hasAudit("settings export failed: 1 wrong password from 127.0.0.1 in 15 min") {
		t.Fatalf("audit = %v", e.auditRows())
	}
	for _, row := range e.auditRows() {
		if strings.HasPrefix(row, "exported settings") {
			t.Fatalf("a refused export was audited as done: %s", row)
		}
	}
	if code, raw := e.export(withKeys); code != 200 || !strings.Contains(raw, `"secrets": {`) {
		t.Fatalf("export with the password = %d %s", code, raw)
	}
	if code, raw := e.export(`{"withoutKeys":true}`); code != 200 {
		t.Fatalf("export without keys = %d %s", code, raw)
	}
	// Wrong passwords count as failed logins from the address.
	for range auth.MaxAttempts {
		e.export(wrong)
	}
	if code, raw := e.export(withKeys); code != 429 {
		t.Fatalf("after %d wrong passwords = %d %s, want 429", auth.MaxAttempts, code, raw)
	}
}

// Neither the file nor the audit log nor the server log ever holds a key, the passphrase or the password.
func TestSettingsSecretsStayOutOfFilesAuditAndLog(t *testing.T) {
	src := newServicesEnv(t)
	file := src.exported(withKeys)
	if strings.Contains(string(file), testKey) {
		t.Fatal("the export holds a key in plain text")
	}
	src.export(`{"passphrase":"` + passphrase + `","password":"not the password"}`)
	dst := newServicesEnv(t)
	dst.importFile(file, "not the passphrase", "preview")
	if code, _, msg := dst.importFile(file, passphrase, "add"); code != 200 {
		t.Fatalf("import = %d %s", code, msg)
	}
	for name, e := range map[string]*servicesEnv{"exporting": src, "importing": dst} {
		audit := strings.Join(e.auditRows(), "\n")
		for _, secret := range []string{passphrase, "not the passphrase", userPassword, "not the password", testKey} {
			if strings.Contains(audit, secret) || strings.Contains(e.logs.String(), secret) {
				t.Errorf("%s server: %q appears in the audit log or the log", name, secret)
			}
		}
	}
}

// A file whose encrypted keys were cut out still imports, without keys, and says which keys are missing.
func TestSettingsFileWithoutItsSecretsImportsNoKeys(t *testing.T) {
	src := newServicesEnv(t)
	var f map[string]any
	if err := json.Unmarshal(src.exported(withKeys), &f); err != nil {
		t.Fatal(err)
	}
	f["secrets"] = nil
	stripped, _ := json.Marshal(f)
	dst := newServicesEnv(t)
	code, rep, msg := dst.importFile(stripped, "", "add")
	if code != 200 || len(rep.Services) != 2 || rep.Services[0].Key != "missing" || rep.Services[1].Key != "none" {
		t.Fatalf("import = %d %+v %s", code, rep, msg)
	}
	svcs, err := dst.srv.Store.Services(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range svcs {
		if svc.SealedKey != nil {
			t.Fatalf("%s got a key", svc.Name)
		}
	}
	if !dst.hasAudit("imported settings: added: 2, skipped: 0") {
		t.Fatalf("audit = %v", dst.auditRows())
	}
}

func TestSettingsImportRefusesTooManyDismissedFinds(t *testing.T) {
	e := newServicesEnv(t)
	file := func(n int) []byte {
		f := backup.File{Format: backup.Format, Version: backup.Version, Services: []backup.Service{}}
		for i := range n {
			f.Dismissed = append(f.Dismissed, fmt.Sprintf("fake http://192.0.2.%d:%d", i%250+1, 1000+i))
		}
		b, _ := json.Marshal(f)
		return b
	}
	if code, _, msg := e.importFile(file(maxDismissed+1), "", "add"); code != 400 ||
		!strings.Contains(msg, "more than 5000 dismissed finds") {
		t.Fatalf("%d dismissed finds = %d %s", maxDismissed+1, code, msg)
	}
	if d, _ := e.srv.Store.Dismissed(context.Background()); len(d) != 0 {
		t.Fatalf("a refused file dismissed %d finds", len(d))
	}
	if code, rep, msg := e.importFile(file(maxDismissed), "", "add"); code != 200 || rep.Dismissed != maxDismissed {
		t.Fatalf("%d dismissed finds = %d %+v %s", maxDismissed, code, rep, msg)
	}
}

// Only a wrong passphrase costs an import attempt: one whose client went away before it was tried does not.
func TestSettingsImportCountsOnlyTriedPassphrases(t *testing.T) {
	e := newServicesEnv(t)
	file := e.exported(withKeys)
	body, _ := json.Marshal(map[string]any{"file": string(file), "passphrase": "not the passphrase", "mode": "preview"})
	attempts := func() int {
		addrs, _ := e.srv.importLimiter.Status()
		if len(addrs) == 0 {
			return 0
		}
		return addrs[0].Attempts
	}
	e.srv.fileKeySlot <- struct{}{} // another settings file's key is being derived
	for range auth.MaxAttempts + 1 {
		ctx, cancel := context.WithCancel(context.Background())
		req, _ := http.NewRequestWithContext(ctx, "POST", e.base+"/api/settings/import", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", e.csrf)
		done := make(chan error, 1)
		go func() {
			resp, err := e.http.Do(req)
			if err == nil {
				resp.Body.Close()
			}
			done <- err
		}()
		eventually(t, "the attempt is reserved", func() bool { return attempts() == 1 })
		cancel()
		if err := <-done; err == nil {
			t.Fatal("the request was answered while it waited")
		}
		eventually(t, "the attempt is given back", func() bool { return attempts() == 0 })
	}
	<-e.srv.fileKeySlot
	if code, _, msg := e.importFile(file, passphrase, "preview"); code != 200 {
		t.Fatalf("import after %d abandoned attempts = %d %s", auth.MaxAttempts+1, code, msg)
	}
}

// Replacing with a file that holds nothing to restore would only remove the saved services: it is refused.
func TestSettingsReplaceWithNothingIsRefused(t *testing.T) {
	e := newServicesEnv(t)
	e.create("Keep")
	file := []byte(`{"format":"observarr-settings","version":1,"createdAt":"","observarrVersion":"","services":[` +
		`{"id":1,"type":"future","name":"Later","url":"http://later:1","container":"","hasKey":false}],` +
		`"dismissed":[],"secrets":null}`)
	if code, rep, msg := e.importFile(file, "", "preview"); code != 200 || rep.Saved != 1 || len(rep.Services) != 0 {
		t.Fatalf("preview = %d %+v %s", code, rep, msg)
	}
	if code, _, msg := e.importFile(file, "", "replace"); code != 400 || !strings.Contains(msg, "nothing replaced") {
		t.Fatalf("replace = %d %s", code, msg)
	}
	if got := e.saved(); len(got) != 1 || got[0] != "Keep" {
		t.Fatalf("saved = %v", got)
	}
}
