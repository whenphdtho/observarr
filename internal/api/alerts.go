package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"observarr/internal/alert"
	"observarr/internal/store"
)

const (
	maxChannelName = 64 // characters
	maxLogLimit    = 200
)

// channelView is an alert channel as the API shows it: never a secret, only which secret fields are saved.
type channelView struct {
	ID        int64           `json:"id"`
	Type      string          `json:"type"`
	Name      string          `json:"name"`
	Target    string          `json:"target"` // where messages go, with nothing secret in it
	Settings  alert.Settings  `json:"settings"`
	HasSecret map[string]bool `json:"hasSecret"`
	Rules     []string        `json:"rules"`
	Enabled   bool            `json:"enabled"`
	Tested    bool            `json:"tested"` // a test got through since the settings or secrets last changed
	Last      *channelResult  `json:"last"`   // the latest delivery or test; nil before the first
	Problem   string          `json:"problem,omitempty"`
}

type channelResult struct {
	At   time.Time `json:"at"`
	OK   bool      `json:"ok"`
	Note string    `json:"note"`
}

// channelInput is a new channel.
type channelInput struct {
	Type     string            `json:"type"`
	Name     string            `json:"name"`
	Settings alert.Settings    `json:"settings"`
	Secrets  map[string]string `json:"secrets"`
	Rules    []string          `json:"rules"` // absent: every rule
}

// channelPatch changes a channel. An absent field stays as it is; in secrets, an absent field is kept and an empty
// one removed. Changing the settings or a secret disables the channel until a test gets through again.
type channelPatch struct {
	Name     *string           `json:"name"`
	Settings *alert.Settings   `json:"settings"`
	Secrets  map[string]string `json:"secrets"`
	Rules    *[]string         `json:"rules"`
	Enabled  *bool             `json:"enabled"`
}

func (s *Server) view(ch store.AlertChannel) channelView {
	v := channelView{ID: ch.ID, Type: ch.Type, Name: ch.Name, HasSecret: map[string]bool{}, Rules: ch.Rules,
		Enabled: ch.Enabled, Tested: ch.Tested}
	for _, name := range alert.SecretNames(ch.Type) {
		v.HasSecret[name] = false
	}
	if !ch.LastAt.IsZero() {
		v.Last = &channelResult{At: ch.LastAt, OK: ch.LastOK, Note: ch.LastNote}
	}
	settings, err := alert.ParseSettings(ch.Settings)
	if err != nil {
		v.Problem = "the saved settings are unreadable; enter them again"
		return v
	}
	v.Settings = settings
	sec, err := alert.OpenSecrets(s.Box, ch.ID, ch.Secrets)
	if err != nil {
		v.Problem = err.Error()
		return v
	}
	for name, val := range sec {
		v.HasSecret[name] = val != ""
	}
	v.Target = alert.Target(ch.Type, settings, sec)
	return v
}

func (s *Server) alertChannels(w http.ResponseWriter, r *http.Request, _ store.Session) {
	chans, err := s.Store.AlertChannels(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := make([]channelView, len(chans))
	for i, ch := range chans {
		out[i] = s.view(ch)
	}
	writeJSON(w, http.StatusOK, out)
}

// createAlertChannel saves a new channel, disabled: it is enabled once a test got through.
func (s *Server) createAlertChannel(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var in channelInput
	if !readJSON(w, r, &in) {
		return
	}
	name, err := channelName(in.Name)
	if err == nil {
		in.Settings, err = alert.CheckChannel(in.Type, in.Settings, in.Secrets)
	}
	var rules []string
	if err == nil {
		rules, err = channelRules(in.Rules)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	settings, err := json.Marshal(in.Settings)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ch, err := s.Store.CreateAlertChannel(r.Context(), store.AlertChannel{Type: in.Type, Name: name,
		Settings: settings, Rules: rules}, func(id int64) ([]byte, error) { return alert.SealSecrets(s.Box, id, in.Secrets) })
	if errors.Is(err, store.ErrExists) {
		writeError(w, http.StatusConflict, "an alert channel with this name already exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, sess.UserName, "added alert channel", ch.Name+" ("+ch.Type+")")
	writeJSON(w, http.StatusCreated, s.view(ch))
}

// updateAlertChannel applies a patch. A changed setting or secret needs a new test before the channel sends again;
// a saved secret goes only to the server it was entered for, so moving it to another needs it again.
func (s *Server) updateAlertChannel(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var p channelPatch
	if !readJSON(w, r, &p) {
		return
	}
	old, ok := s.channelByID(w, r)
	if !ok {
		return
	}
	settings, err := alert.ParseSettings(old.Settings)
	if err != nil {
		settings = alert.Settings{}
	}
	sec, secErr := alert.OpenSecrets(s.Box, old.ID, old.Secrets)
	if secErr != nil {
		sec = alert.Secrets{}
	}
	ch := old
	if p.Name != nil {
		if ch.Name, err = channelName(*p.Name); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if p.Rules != nil {
		if ch.Rules, err = channelRules(*p.Rules); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	changed := p.Settings != nil || len(p.Secrets) > 0
	if p.Settings != nil {
		moved := p.Settings.Server != settings.Server || p.Settings.Host != settings.Host
		for name, v := range sec {
			if _, entered := p.Secrets[name]; moved && v != "" && !entered {
				writeError(w, http.StatusBadRequest, "enter the "+name+" again when changing the server")
				return
			}
		}
		settings = *p.Settings
	}
	if secErr != nil && !changed {
		writeError(w, http.StatusUnprocessableEntity, secErr.Error())
		return
	}
	newSec := maps.Clone(sec)
	maps.Copy(newSec, p.Secrets)
	if changed {
		if settings, err = alert.CheckChannel(ch.Type, settings, newSec); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if ch.Settings, err = json.Marshal(settings); err != nil {
			s.internal(w, r, err)
			return
		}
		if ch.Secrets, err = alert.SealSecrets(s.Box, ch.ID, newSec); err != nil {
			s.internal(w, r, err)
			return
		}
		ch.Enabled, ch.Tested = false, false
	}
	if p.Enabled != nil && *p.Enabled != ch.Enabled {
		if *p.Enabled && !ch.Tested {
			writeError(w, http.StatusConflict, "send a test first: a channel is enabled once a test got through")
			return
		}
		ch.Enabled = *p.Enabled
	}
	switch err := s.Store.UpdateAlertChannel(r.Context(), ch, old); {
	case errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, "an alert channel with this name already exists")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such alert channel")
		return
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "the channel was changed meanwhile; reload and try again")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.reloadAlerts(r)
	detail := ch.Name
	switch {
	case changed:
		detail += ": settings changed, disabled until tested"
	case ch.Enabled && !old.Enabled:
		detail += ": enabled"
	case !ch.Enabled && old.Enabled:
		detail += ": disabled"
	}
	s.audit(r, sess.UserName, "changed alert channel", detail)
	if ch, err = s.Store.AlertChannelByID(r.Context(), ch.ID); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(ch))
}

func (s *Server) deleteAlertChannel(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ch, ok := s.channelByID(w, r)
	if !ok {
		return
	}
	switch err := s.Store.DeleteAlertChannel(r.Context(), ch.ID); {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such alert channel")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.reloadAlerts(r)
	s.audit(r, sess.UserName, "removed alert channel", ch.Name)
	w.WriteHeader(http.StatusNoContent)
}

// testAlertChannel sends a test message now and answers with the result; a channel that passes may be enabled.
func (s *Server) testAlertChannel(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ch, ok := s.channelByID(w, r)
	if !ok {
		return
	}
	passed, note := s.Alerts.Test(r.Context(), ch)
	result := ": delivered"
	if !passed {
		result = ": failed"
	}
	s.audit(r, sess.UserName, "tested alert channel", ch.Name+result)
	if ch, err := s.Store.AlertChannelByID(r.Context(), ch.ID); err == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": passed, "note": note, "channel": s.view(ch)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": passed, "note": note})
}

func (s *Server) alertRules(w http.ResponseWriter, _ *http.Request, _ store.Session) {
	writeJSON(w, http.StatusOK, s.Alerts.Rules())
}

func (s *Server) setAlertRules(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var in alert.Rules
	if !readJSON(w, r, &in) {
		return
	}
	if err := in.Check(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Alerts.SetRules(r.Context(), in); err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, sess.UserName, "changed alert rules", "")
	writeJSON(w, http.StatusOK, in)
}

// alertLog answers with the newest alerts sent, at most ?limit= (default 50, at most 200).
func (s *Server) alertLog(w http.ResponseWriter, r *http.Request, _ store.Session) {
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 1 || n > maxLogLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit: 1 to %d", maxLogLimit))
			return
		}
		limit = n
	}
	entries, err := s.Store.AlertLog(r.Context(), limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type delivery struct {
		ChannelID int64     `json:"channelId"`
		Channel   string    `json:"channel"`
		Status    string    `json:"status"`
		Attempts  int       `json:"attempts"`
		Note      string    `json:"note"`
		At        time.Time `json:"at"`
	}
	type entry struct {
		ID         int64      `json:"id"`
		At         time.Time  `json:"at"`
		Event      string     `json:"event"`
		Rule       string     `json:"rule"`
		Severity   string     `json:"severity"`
		Service    string     `json:"service"`
		Title      string     `json:"title"`
		Message    string     `json:"message"`
		Deliveries []delivery `json:"deliveries"`
	}
	out := make([]entry, len(entries))
	for i, e := range entries {
		out[i] = entry{ID: e.ID, At: e.At, Event: e.Event, Rule: e.Rule, Severity: e.Severity, Service: e.Service,
			Title: e.Title, Message: e.Message, Deliveries: make([]delivery, len(e.Deliveries))}
		for j, d := range e.Deliveries {
			out[i].Deliveries[j] = delivery(d)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// alertsFiring answers with the alerts firing now, worst first, for the shell's indicator.
func (s *Server) alertsFiring(w http.ResponseWriter, _ *http.Request, _ store.Session) {
	type firing struct {
		Key      string    `json:"key"`
		Rule     string    `json:"rule"`
		Severity string    `json:"severity"`
		Title    string    `json:"title"`
		Message  string    `json:"message"`
		View     string    `json:"view"`
		Since    time.Time `json:"since"`
	}
	list := s.Alerts.Firing()
	out := make([]firing, len(list))
	for i, a := range list {
		out[i] = firing{Key: a.Key, Rule: string(a.Rule), Severity: string(a.Severity), Title: a.Title,
			Message: a.Message, View: a.View, Since: a.Since}
	}
	writeJSON(w, http.StatusOK, out)
}

// channelByID loads the channel named by the {id} path value, answering 404 or 500 itself when it cannot.
func (s *Server) channelByID(w http.ResponseWriter, r *http.Request) (store.AlertChannel, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such alert channel")
		return store.AlertChannel{}, false
	}
	ch, err := s.Store.AlertChannelByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such alert channel")
		return store.AlertChannel{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return store.AlertChannel{}, false
	}
	return ch, true
}

// reloadAlerts puts saved changes of channels or rules in force.
func (s *Server) reloadAlerts(r *http.Request) {
	if err := s.Alerts.Reload(r.Context()); err != nil {
		s.Log.Error("reloading the alert channels failed", "err", err)
	}
}

// channelName checks a channel's name: 1 to 64 characters, no control characters, trimmed.
func channelName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > maxChannelName || strings.ContainsFunc(name, unicode.IsControl) {
		return "", fmt.Errorf("name: 1 to %d characters", maxChannelName)
	}
	return name, nil
}

// channelRules checks the rules a channel receives; nil means every rule.
func channelRules(in []string) ([]string, error) {
	if in == nil {
		out := make([]string, len(alert.AllRules))
		for i, r := range alert.AllRules {
			out[i] = string(r)
		}
		return out, nil
	}
	out := []string{}
	for _, r := range in {
		if !slices.Contains(alert.AllRules, alert.Rule(r)) {
			return nil, fmt.Errorf("rules: unknown rule %q", r)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out, nil
}
