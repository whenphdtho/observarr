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
	// maxDismissedKey bounds one dismissed find in a settings file, in bytes.
	maxDismissedKey = 4096
)

// exportInput asks for the settings file, with the keys encrypted under passphrase or without them.
type exportInput struct {
	Passphrase  string `json:"passphrase"`
	WithoutKeys bool   `json:"withoutKeys"`
}

// exportSettings answers with the settings file as a download: every service and dismissed find, and, under a
// passphrase, the services' keys.
func (s *Server) exportSettings(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var in exportInput
	if !readJSON(w, r, &in) {
		return
	}
	switch {
	case in.WithoutKeys && in.Passphrase != "":
		writeError(w, http.StatusBadRequest, "passphrase: not with withoutKeys")
		return
	case !in.WithoutKeys:
		if err := backup.CheckPassphrase(in.Passphrase); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
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
	if !in.WithoutKeys {
		if !s.withHashSlot(ctx, func() { err = backup.Seal(&f, keys, in.Passphrase) }) {
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
	s.audit(r, sess.UserName, "exported settings", detail)
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Disposition", `attachment; filename="observarr-settings-`+now.Format("2006-01-02")+`.json"`)
	w.WriteHeader(http.StatusOK)
	w.Write(append(body, '\n'))
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
	Skipped          []skippedService  `json:"skipped"`
	Dismissed        int               `json:"dismissed"` // dismissed finds of known app types
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
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Mode == "preview" {
		writeJSON(w, http.StatusOK, rep)
		return
	}

	replace := in.Mode == "replace"
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
	created, err := s.Store.ImportServices(ctx, replace, rows, dismissed,
		func(i int, id int64) ([]byte, error) { return s.sealKey(id, items[i].sp.target.APIKey) })
	if errors.Is(err, store.ErrExists) {
		writeError(w, http.StatusConflict, "a service with one of these names exists already")
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
	rep.Applied = true

	detail := fmt.Sprintf("added: %d, skipped: %d", len(created), len(rep.Skipped))
	if replace {
		detail += fmt.Sprintf(", removed: %d", rep.Removed)
	}
	if keys != nil {
		detail += ", keys decrypted"
	}
	s.audit(r, sess.UserName, "imported settings", detail)
	writeJSON(w, http.StatusOK, rep)
}

// importKeys returns the keys in f, decrypted with the request's passphrase; none when f holds none or the user
// imports without them. A wrong passphrase counts against the user's import attempts. It answers the request
// itself when it fails.
func (s *Server) importKeys(w http.ResponseWriter, r *http.Request, sess store.Session, f backup.File,
	in importInput) (map[int64]string, bool) {
	switch {
	case in.WithoutKeys && in.Passphrase != "":
		writeError(w, http.StatusBadRequest, "passphrase: not with withoutKeys")
		return nil, false
	case f.Secrets == nil || in.WithoutKeys:
		return nil, true
	case in.Passphrase == "":
		writeError(w, http.StatusBadRequest, "this file holds encrypted keys: enter its passphrase, or import without keys")
		return nil, false
	case len(in.Passphrase) > backup.MaxPassphrase:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("passphrase: at most %d bytes", backup.MaxPassphrase))
		return nil, false
	}
	who := strings.ToLower(sess.UserName)
	if ok, wait := s.importLimiter.Attempt(who); !ok {
		tooMany(w, wait, "too many wrong passphrases")
		return nil, false
	}
	var keys map[int64]string
	var err error
	if !s.withHashSlot(r.Context(), func() { keys, err = backup.Open(f, in.Passphrase) }) {
		return nil, false
	}
	if errors.Is(err, backup.ErrPassphrase) {
		s.auditFailure(r, sess.UserName, "settings import failed", "wrong passphrase",
			auth.ClientIP(r, s.Config.TrustedProxies).String())
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return nil, false
	}
	s.importLimiter.Reset(r.Context(), who) // in memory only: it cannot fail
	return keys, true
}

// planImport checks every service in f as adding it by hand would, without contacting it: services of an
// unknown app type are skipped, any other fault fails the whole file. It returns the services to add, the report
// of a preview (NameTaken set against saved), and the dismissed finds of known app types.
func (s *Server) planImport(f backup.File, keys map[int64]string, saved []store.Service) ([]importItem, importReport,
	[]string, error) {
	taken := make(map[string]bool, len(saved))
	for _, svc := range saved {
		taken[strings.ToLower(svc.Name)] = true
	}
	rep := importReport{CreatedAt: f.CreatedAt, ObservarrVersion: f.ObservarrVersion,
		Services: []importedService{}, Skipped: []skippedService{}}
	var items []importItem
	inFile := map[string]bool{}
	for _, svc := range f.Services {
		if _, ok := s.Registry.Get(svc.Type); !ok {
			rep.Skipped = append(rep.Skipped, skippedService{Type: svc.Type, Name: svc.Name, URL: svc.URL,
				Reason: "unknown app type"})
			continue
		}
		key := keys[svc.ID]
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
