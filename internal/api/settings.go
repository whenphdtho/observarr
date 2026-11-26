package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"observarr/internal/auth"
	"observarr/internal/backup"
	"observarr/internal/shape"
	"observarr/internal/store"
)

const (
	// maxImportBody bounds an import request: the file as a JSON string, escaping included, and the passphrase.
	maxImportBody = 2*backup.MaxFile + 64<<10
	// maxDismissedKey bounds one dismissed find in a settings file, in bytes; maxDismissed how many a file may hold.
	maxDismissedKey = 4096
	maxDismissed    = 5000
)

// exportInput asks for the settings file, with the keys encrypted under passphrase or without them. The keys
// leave the server only with the account's current password.
type exportInput struct {
	Passphrase  string `json:"passphrase"`
	Password    string `json:"password"`
	WithoutKeys bool   `json:"withoutKeys"`
}

// exportSettings answers with the settings file as a download: every service, dismissed find, alert rule and alert
// channel, and, under a passphrase, the services' keys and the channels' secrets.
func (s *Server) exportSettings(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var in exportInput
	if !readJSON(w, r, &in) {
		return
	}
	switch {
	case in.WithoutKeys && in.Passphrase != "":
		writeError(w, http.StatusBadRequest, "passphrase: not with withoutKeys")
		return
	case in.WithoutKeys && in.Password != "":
		writeError(w, http.StatusBadRequest, "password: not with withoutKeys")
		return
	case !in.WithoutKeys:
		if err := backup.CheckPassphrase(in.Passphrase); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if !s.confirmPassword(w, r, sess, in.Password) {
			return
		}
	}
	ctx := r.Context()
	svcs, err := s.Store.Services(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	dismissed, err := s.Store.Dismissed(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}

	now := time.Now()
	f := backup.File{Format: backup.Format, Version: backup.Version, CreatedAt: now.UTC().Format(time.RFC3339),
		ObservarrVersion: s.Version, Services: make([]backup.Service, 0, len(svcs)),
		Dismissed: make([]string, 0, len(dismissed))}
	keys := map[int64]string{}
	for _, svc := range svcs {
		f.Services = append(f.Services, backup.Service{ID: svc.ID, Type: svc.Type, Name: svc.Name, URL: svc.BaseURL,
			Container: svc.Container, HasKey: len(svc.SealedKey) > 0})
		// A key the box cannot open is left out; the file says it must be entered again.
		if key, err := s.openKey(svc); !in.WithoutKeys && err == nil && key != "" {
			keys[svc.ID] = key
		}
	}
	for key := range dismissed {
		f.Dismissed = append(f.Dismissed, key)
	}
	slices.Sort(f.Dismissed)
	alerts, channelSecrets, err := s.exportAlerts(ctx, !in.WithoutKeys)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	f.Alerts = alerts
	if !in.WithoutKeys {
		sealed := backup.Keys{Services: keys, Channels: channelSecrets}
		if !s.withFileKeySlot(ctx, func() { err = backup.Seal(&f, sealed, in.Passphrase) }) {
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
	}
	body, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		s.internal(w, r, err)
		return
	}

	detail := fmt.Sprintf("services: %d, keys encrypted: %d", len(f.Services), len(keys))
	if in.WithoutKeys {
		detail = fmt.Sprintf("services: %d, without keys", len(f.Services))
	}
	if n := len(alerts.Channels); n > 0 {
		detail += fmt.Sprintf(", alert channels: %d", n)
	}
	s.audit(r, sess.UserName, "exported settings", detail)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Disposition", `attachment; filename="observarr-settings-`+now.Format("2006-01-02")+`.json"`)
	w.WriteHeader(http.StatusOK)
	w.Write(append(body, '\n'))
}

// confirmPassword checks the session user's current password before the keys leave the server, so a session
// alone (a stolen cookie, or a script running in the page) cannot take them out. Each check counts against the
// client address's login attempts, as a login does, and a wrong password is audited (without it). It answers
// the request itself when it fails.
func (s *Server) confirmPassword(w http.ResponseWriter, r *http.Request, sess store.Session, password string) bool {
	switch {
	case password == "":
		writeError(w, http.StatusBadRequest, "password: enter your password to export the keys")
		return false
	case len(password) > maxPasswordLen:
		writeError(w, http.StatusBadRequest, "password: at most 1024 bytes")
		return false
	}
	u, err := s.Store.UserByName(r.Context(), sess.UserName)
	if err != nil {
		s.internal(w, r, err)
		return false
	}
	if u.PasswordHash == noPassword {
		writeError(w, http.StatusForbidden,
			"this account signs in through the login proxy and has no password here: export without keys")
		return false
	}
	key := auth.LimitKey(auth.ClientIP(r, s.Config.TrustedProxies))
	if !s.attempt(w, key) {
		return false
	}
	var match bool
	if !s.withHashSlot(r.Context(), func() { match = auth.CheckPassword(password, u.PasswordHash) }) {
		return false
	}
	if !match {
		s.auditFailure(r, sess.UserName, "settings export failed", "wrong password", key)
		writeError(w, http.StatusForbidden, "wrong password")
		return false
	}
	s.succeeded(r, key)
	return true
}

// importInput is a settings file to preview or apply. Mode "preview" changes nothing; "add" adds the file's
// services alongside the saved ones, skipping any whose name is taken; "replace" removes the saved ones first.
type importInput struct {
	File        string `json:"file"`
	Passphrase  string `json:"passphrase"`
	WithoutKeys bool   `json:"withoutKeys"` // import a file with encrypted keys without them
	Mode        string `json:"mode"`
}

// importReport is what an import adds, or would add.
type importReport struct {
	CreatedAt        string            `json:"createdAt"`
	ObservarrVersion string            `json:"observarrVersion"`
	Services         []importedService `json:"services"`
	Channels         []importedChannel `json:"channels"` // alert channels
	Skipped          []skippedService  `json:"skipped"`
	Dismissed        int               `json:"dismissed"` // dismissed finds of known app types
	Saved            int               `json:"saved"`     // services saved here before the import
	Removed          int               `json:"removed"`   // saved services removed (applied "replace")
	Applied          bool              `json:"applied"`
}

type importedService struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Container string `json:"container,omitempty"`
	Key       string `json:"key"`                 // "included"; "missing": to be entered again; "none"
	NameTaken bool   `json:"nameTaken,omitempty"` // in a preview: a saved service has this name, so "add" skips it
}

type skippedService struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Reason string `json:"reason"`
}

// importItem is one service of the file, checked as adding it by hand would check it.
type importItem struct {
	sp  spec
	typ string
	out importedService
}

// importSettings previews or applies a settings file. Applying writes everything in one transaction; the
// imported services are polled from then on like saved ones, and contacted by nothing else.
func (s *Server) importSettings(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var in importInput
	if !readJSONUpTo(w, r, &in, maxImportBody) {
		return
	}
	if in.Mode != "preview" && in.Mode != "add" && in.Mode != "replace" {
		writeError(w, http.StatusBadRequest, "mode: preview, add or replace")
		return
	}
	if len(in.File) > backup.MaxFile {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("the file is larger than %d MiB", backup.MaxFile>>20))
		return
	}
	f, err := backup.Parse([]byte(in.File))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	keys, ok := s.importKeys(w, r, sess, f, in)
	if !ok {
		return
	}

	if in.Mode != "preview" {
		s.servicesMu.Lock()
		defer s.servicesMu.Unlock()
	}
	ctx := r.Context()
	saved, err := s.Store.Services(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	items, rep, dismissed, err := s.planImport(f, keys, saved)
	var alerts alertPlan
	if err == nil {
		alerts, err = s.planAlerts(ctx, f, keys)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rep.Saved = len(saved)
	rep.Channels = append(rep.Channels, alerts.report...)
	rep.Skipped = append(rep.Skipped, alerts.skipped...)
	if in.Mode == "preview" {
		writeJSON(w, http.StatusOK, rep)
		return
	}

	replace := in.Mode == "replace"
	if replace && len(items) == 0 { // replacing with nothing would only remove every saved service
		writeError(w, http.StatusBadRequest, "the file holds no app this server can restore: nothing replaced")
		return
	}
	if !replace {
		items = slices.DeleteFunc(items, func(it importItem) bool {
			if it.out.NameTaken {
				rep.Skipped = append(rep.Skipped, skippedService{Type: it.typ, Name: it.out.Name, URL: it.out.URL,
					Reason: "a service with this name exists already"})
			}
			return it.out.NameTaken
		})
	}
	rows := make([]store.Service, len(items))
	for i, it := range items {
		rows[i] = store.Service{Type: it.typ, Name: it.sp.name, BaseURL: it.sp.target.BaseURL.String(),
			Container: it.sp.container}
	}
	alerts.skipped = nil
	imported := alerts.apply(s, replace)
	created, err := s.Store.ImportServices(ctx, replace, rows, dismissed,
		func(i int, id int64) ([]byte, error) { return s.sealKey(id, items[i].sp.target.APIKey) }, imported)
	if errors.Is(err, store.ErrExists) {
		writeError(w, http.StatusConflict, "a service or alert channel with one of these names exists already")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}

	if replace {
		for _, old := range saved {
			s.Poller.Stop(old.ID)
			s.Hub.Remove(old.ID)
		}
		rep.Removed = len(saved)
	}
	// Not cancelled with the request: the services are saved, so their suggestions must go too.
	bg := context.WithoutCancel(ctx)
	rep.Services = make([]importedService, len(items))
	for i, svc := range created {
		s.startSaved(svc)
		if err := s.discovery.Forget(bg, svc.Type, items[i].sp.target.BaseURL, items[i].sp.target.APIKey); err != nil {
			s.Log.Warn("imported app may be suggested again", "service", svc.Name, "err", err)
		}
		rep.Services[i] = items[i].out
		rep.Services[i].NameTaken = false
	}
	rep.Channels = alerts.report
	rep.Skipped = append(rep.Skipped, alerts.skipped...)
	if imported != nil && s.Alerts != nil {
		if err := s.Alerts.Reload(bg); err != nil {
			s.Log.Error("reloading the alert channels failed", "err", err)
		}
	}
	rep.Applied = true

	detail := fmt.Sprintf("added: %d, skipped: %d", len(created), len(rep.Skipped))
	if replace {
		detail += fmt.Sprintf(", removed: %d", rep.Removed)
	}
	if imported != nil && len(imported.Channels) > 0 {
		detail += fmt.Sprintf(", alert channels: %d", len(imported.Channels))
	}
	if f.Secrets != nil && !in.WithoutKeys {
		detail += ", keys decrypted"
	}
	s.audit(r, sess.UserName, "imported settings", detail)
	writeJSON(w, http.StatusOK, rep)
}

// importKeys returns the keys and secrets in f, decrypted with the request's passphrase; none when f holds none or
// the user imports without them. A wrong passphrase counts against the user's import attempts. It answers the
// request itself when it fails.
func (s *Server) importKeys(w http.ResponseWriter, r *http.Request, sess store.Session, f backup.File,
	in importInput) (backup.Keys, bool) {
	switch {
	case in.WithoutKeys && in.Passphrase != "":
		writeError(w, http.StatusBadRequest, "passphrase: not with withoutKeys")
		return backup.Keys{}, false
	case f.Secrets == nil || in.WithoutKeys:
		return backup.Keys{}, true
	case in.Passphrase == "":
		writeError(w, http.StatusBadRequest, "this file holds encrypted keys: enter its passphrase, or import without keys")
		return backup.Keys{}, false
	case len(in.Passphrase) > backup.MaxPassphrase:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("passphrase: at most %d bytes", backup.MaxPassphrase))
		return backup.Keys{}, false
	}
	who := strings.ToLower(sess.UserName)
	if ok, wait := s.importLimiter.Attempt(who); !ok {
		tooMany(w, wait, "too many wrong passphrases")
		return backup.Keys{}, false
	}
	var keys backup.Keys
	var err error
	if !s.withFileKeySlot(r.Context(), func() { keys, err = backup.Open(f, in.Passphrase) }) {
		s.importLimiter.Release(who) // the client went away before the passphrase was tried
		return backup.Keys{}, false
	}
	if errors.Is(err, backup.ErrPassphrase) {
		s.auditFailure(r, sess.UserName, "settings import failed", "wrong passphrase",
			auth.ClientIP(r, s.Config.TrustedProxies).String())
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return backup.Keys{}, false
	}
	if err != nil { // only a wrong passphrase costs an attempt
		s.importLimiter.Release(who)
		writeError(w, http.StatusBadRequest, err.Error())
		return backup.Keys{}, false
	}
	s.importLimiter.Reset(r.Context(), who) // in memory only: it cannot fail
	return keys, true
}

// planImport checks every service in f as adding it by hand would, without contacting it: services of an
// unknown app type are skipped, any other fault fails the whole file. It returns the services to add, the report
// of a preview (NameTaken set against saved), and the dismissed finds of known app types.
func (s *Server) planImport(f backup.File, keys backup.Keys, saved []store.Service) ([]importItem, importReport,
	[]string, error) {
	taken := make(map[string]bool, len(saved))
	for _, svc := range saved {
		taken[strings.ToLower(svc.Name)] = true
	}
	rep := importReport{CreatedAt: f.CreatedAt, ObservarrVersion: f.ObservarrVersion,
		Services: []importedService{}, Channels: []importedChannel{}, Skipped: []skippedService{}}
	var items []importItem
	inFile := map[string]bool{}
	for _, svc := range f.Services {
		if _, ok := s.Registry.Get(svc.Type); !ok {
			rep.Skipped = append(rep.Skipped, skippedService{Type: svc.Type, Name: svc.Name, URL: svc.URL,
				Reason: "unknown app type"})
			continue
		}
		key := keys.Services[svc.ID]
		sp, err := s.parseTarget(targetInput{Type: svc.Type, URL: svc.URL, APIKey: key})
		if err == nil {
			sp, err = sp.named(svc.Name, svc.Container)
		}
		if err != nil {
			return nil, importReport{}, nil, fmt.Errorf("service %q: %v", shape.ClipTo(svc.Name, maxServiceName), err)
		}
		lower := strings.ToLower(sp.name)
		if inFile[lower] {
			return nil, importReport{}, nil, fmt.Errorf("the file names two services %q", sp.name)
		}
		inFile[lower] = true
		out := importedService{Type: svc.Type, Name: sp.name, URL: sp.target.BaseURL.String(), Container: sp.container,
			Key: "none", NameTaken: taken[lower]}
		switch {
		case key != "":
			out.Key = "included"
		case svc.HasKey:
			out.Key = "missing"
		}
		items = append(items, importItem{sp: sp, typ: svc.Type, out: out})
		rep.Services = append(rep.Services, out)
	}

	if len(f.Dismissed) > maxDismissed {
		return nil, importReport{}, nil, fmt.Errorf("the file holds more than %d dismissed finds", maxDismissed)
	}
	var dismissed []string
	for i, key := range f.Dismissed {
		typ, rest, ok := strings.Cut(key, " ")
		if !ok || rest == "" || len(key) > maxDismissedKey || !utf8.ValidString(key) ||
			strings.ContainsFunc(key, unicode.IsControl) {
			return nil, importReport{}, nil, fmt.Errorf("malformed settings file: dismissed find %d", i+1)
		}
		if _, known := s.Registry.Get(typ); known {
			dismissed = append(dismissed, key)
		}
	}
	rep.Dismissed = len(dismissed)
	return items, rep, dismissed, nil
}
