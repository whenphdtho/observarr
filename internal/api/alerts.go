package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	// Sent again is not changed: what counts is whether the settings or secrets differ from the saved ones.
	sent := p.Settings != nil || len(p.Secrets) > 0
	was := settings
	if p.Settings != nil {
		next, err := alert.CheckSettings(ch.Type, *p.Settings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Compared as checked, so "https://NTFY.sh:443" is the server it was and another server is not.
		if moved := next.Server != settings.Server || next.Host != settings.Host; moved {
			if name := unentered(sec, p.Secrets, nil); name != "" {
				writeError(w, http.StatusBadRequest, "enter the "+name+" again when changing the server")
				return
			}
		}
		settings = next
	}
	// A webhook's signing secret goes only to the address it was entered for.
	if u, ok := p.Secrets["url"]; ok && ch.Type == "webhook" && u != sec["url"] {
		if name := unentered(sec, p.Secrets, []string{"secret"}); name != "" {
			writeError(w, http.StatusBadRequest, "enter the "+name+" again when changing the address")
			return
		}
	}
	if secErr != nil && !sent {
		writeError(w, http.StatusUnprocessableEntity, secErr.Error())
		return
	}
	newSec := maps.Clone(sec)
	maps.Copy(newSec, p.Secrets)
	changed := false
	if sent {
		if settings, err = alert.CheckChannel(ch.Type, settings, newSec); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		changed = secErr != nil || !reflect.DeepEqual(settings, was) || !maps.Equal(newSec, sec)
	}
	if changed {
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

// unentered names a saved secret, of names (all when nil), that entered does not hold again; "" when none.
func unentered(saved, entered map[string]string, names []string) string {
	for _, name := range slices.Sorted(maps.Keys(saved)) {
		if _, ok := entered[name]; saved[name] != "" && !ok && (names == nil || slices.Contains(names, name)) {
			return name
		}
	}
	return ""
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
// One test runs per channel at a time, and a session sends at most testsPerMinute.
func (s *Server) testAlertChannel(w http.ResponseWriter, r *http.Request, sess store.Session) {
	ch, ok := s.channelByID(w, r)
	if !ok {
		return
	}
	done, err := s.alertTests.start(ch.ID, sess.CSRF, time.Now())
	if err != nil {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusTooManyRequests, err.Error())
		return
	}
	defer done()
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
	// A rule the request leaves out keeps its value: only an explicit false switches one off.
	cur := s.Alerts.Rules()
	if in.Enabled == nil {
		in.Enabled = map[alert.Rule]bool{}
	}
	for _, rule := range alert.AllRules {
		if _, ok := in.Enabled[rule]; !ok {
			in.Enabled[rule] = cur.Enabled[rule]
		}
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

// alertAttention answers with what needs attention now, as the live stream's "attention" event does: for a page
// that has no stream (its session has every one it may have open) and asks now and then instead.
func (s *Server) alertAttention(w http.ResponseWriter, _ *http.Request, _ store.Session) {
	items := s.Alerts.Attention()
	if items == nil {
		items = []alert.Item{}
	}
	writeJSON(w, http.StatusOK, items)
}

// muteAlert silences one thing that needs attention, by its key, for an hour, eight hours or a day. It stays
// listed, with when its mute ends, but sends nothing and is not counted.
func (s *Server) muteAlert(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var in struct {
		Key     string `json:"key"`
		Minutes int    `json:"minutes"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	span := time.Duration(in.Minutes) * time.Minute
	if !slices.Contains(alert.MuteSpans, span) {
		writeError(w, http.StatusBadRequest, "minutes: 60, 480 or 1440")
		return
	}
	until, err := s.Alerts.Mute(r.Context(), in.Key, span, sess.UserName)
	switch {
	case errors.Is(err, alert.ErrNotListed):
		writeError(w, http.StatusNotFound, "nothing that needs attention has this key")
		return
	case errors.Is(err, store.ErrTooMany):
		writeError(w, http.StatusConflict, fmt.Sprintf("at most %d mutes at once: unmute some first", store.MaxAlertMutes))
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	s.audit(r, sess.UserName, "muted alert", fmt.Sprintf("%s for %d min", in.Key, in.Minutes))
	writeJSON(w, http.StatusOK, map[string]any{"key": in.Key, "mutedUntil": until})
}

// unmuteAlert ends the mute of ?key=. A key is free text (a path, a "#1"), so it travels as a query value, not as
// part of the path. Without such a mute it is a 404 and nothing is noted.
func (s *Server) unmuteAlert(w http.ResponseWriter, r *http.Request, sess store.Session) {
	key := r.URL.Query().Get("key")
	was, err := s.Alerts.Unmute(r.Context(), key)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if !was {
		writeError(w, http.StatusNotFound, "nothing with this key is muted")
		return
	}
	s.audit(r, sess.UserName, "unmuted alert", key)
	w.WriteHeader(http.StatusNoContent)
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

const (
	testsPerMinute  = 6
	maxTestSessions = 64 // sessions whose recent tests are remembered; beyond, the oldest are forgotten
)

// alertTests limits the tests sent by hand: one at a time per channel, testsPerMinute per session.
type alertTests struct {
	mu      sync.Mutex
	running map[int64]bool
	recent  map[string][]time.Time // by session (its CSRF token): when its tests of the last minute began
}

// start begins a test of channel id for session key at now, or says why it may not; done ends it.
func (t *alertTests) start(id int64, key string, now time.Time) (done func(), err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running == nil {
		t.running, t.recent = map[int64]bool{}, map[string][]time.Time{}
	}
	if t.running[id] {
		return nil, errors.New("a test of this channel is under way")
	}
	recent := slices.DeleteFunc(t.recent[key], func(at time.Time) bool { return now.Sub(at) >= time.Minute })
	if len(recent) >= testsPerMinute {
		return nil, fmt.Errorf("at most %d tests a minute; wait a moment", testsPerMinute)
	}
	if _, known := t.recent[key]; !known && len(t.recent) >= maxTestSessions {
		for k, times := range t.recent {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= time.Minute {
				delete(t.recent, k)
			}
		}
		if len(t.recent) >= maxTestSessions {
			return nil, errors.New("too many tests at once; wait a moment")
		}
	}
	t.recent[key] = append(recent, now)
	t.running[id] = true
	return func() {
		t.mu.Lock()
		delete(t.running, id)
		t.mu.Unlock()
	}, nil
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
