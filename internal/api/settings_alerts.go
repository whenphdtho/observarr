package api

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"observarr/internal/alert"
	"observarr/internal/backup"
	"observarr/internal/shape"
	"observarr/internal/store"
)

// maxImportChannels bounds the alert channels a settings file may hold.
const maxImportChannels = 100

// importedChannel is an alert channel a settings import adds, or would add.
type importedChannel struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Secrets   string `json:"secrets"`             // "included"; "missing": to be entered again; "none"
	Enabled   bool   `json:"enabled"`             // it sends at once: it was enabled and has what it needs
	NameTaken bool   `json:"nameTaken,omitempty"` // in a preview: a saved channel has this name, so "add" skips it
}

// exportAlerts returns the alert rules and channels for a settings file and, with withSecrets, the channels'
// secrets by channel ID. A channel whose secrets cannot be opened is exported without them.
func (s *Server) exportAlerts(ctx context.Context, withSecrets bool) (*backup.Alerts, map[int64]map[string]string, error) {
	chans, err := s.Store.AlertChannels(ctx)
	if err != nil {
		return nil, nil, err
	}
	rules, err := s.Store.AlertRules(ctx)
	if err != nil {
		return nil, nil, err
	}
	if rules == nil {
		if rules, err = json.Marshal(alert.DefaultRules()); err != nil {
			return nil, nil, err
		}
	}
	out := &backup.Alerts{Rules: rules, Channels: make([]backup.Channel, 0, len(chans))}
	secrets := map[int64]map[string]string{}
	for _, ch := range chans {
		c := backup.Channel{ID: ch.ID, Type: ch.Type, Name: ch.Name, Settings: ch.Settings, Rules: ch.Rules,
			Enabled: ch.Enabled, Secrets: []string{}}
		sec, err := alert.OpenSecrets(s.Box, ch.ID, ch.Secrets)
		if err != nil {
			c.Secrets = alert.SecretNames(ch.Type) // which of them it had is unknown: each must be entered again
		}
		for name, v := range sec {
			if v != "" {
				c.Secrets = append(c.Secrets, name)
			}
		}
		slices.Sort(c.Secrets)
		if withSecrets && err == nil && len(sec) > 0 {
			secrets[ch.ID] = sec
		}
		out.Channels = append(out.Channels, c)
	}
	return out, secrets, nil
}

// alertPlan is the alert part of a settings import, checked as saving it by hand would check it.
type alertPlan struct {
	rules    []byte // nil: the file has no alerts
	channels []store.AlertChannel
	secrets  []alert.Secrets
	report   []importedChannel
	skipped  []skippedService
}

// planAlerts checks the alerts of f. Channels of an unknown type are skipped; any other fault fails the file. A
// channel keeps its secrets only when the file brought them, and is enabled only when it was and has every secret
// it had.
func (s *Server) planAlerts(ctx context.Context, f backup.File, keys backup.Keys) (alertPlan, error) {
	var p alertPlan
	if f.Alerts == nil {
		return p, nil
	}
	r, err := alert.ParseRules(f.Alerts.Rules)
	if err != nil {
		return p, fmt.Errorf("alert rules: %v", err)
	}
	if p.rules, err = json.Marshal(r); err != nil {
		return p, err
	}
	if len(f.Alerts.Channels) > maxImportChannels {
		return p, fmt.Errorf("the file holds more than %d alert channels", maxImportChannels)
	}
	saved, err := s.Store.AlertChannels(ctx)
	if err != nil {
		return p, err
	}
	taken := map[string]bool{}
	for _, ch := range saved {
		taken[strings.ToLower(ch.Name)] = true
	}
	inFile := map[string]bool{}
	for _, c := range f.Alerts.Channels {
		if !slices.Contains(alert.Types, c.Type) {
			p.skipped = append(p.skipped, skippedService{Type: c.Type, Name: c.Name, Reason: "unknown alert channel type"})
			continue
		}
		ch, sec, out, err := planChannel(c, keys.Channels[c.ID])
		if err != nil {
			return p, fmt.Errorf("alert channel %q: %v", shape.ClipTo(c.Name, maxChannelName), err)
		}
		lower := strings.ToLower(ch.Name)
		if inFile[lower] {
			return p, fmt.Errorf("the file names two alert channels %q", ch.Name)
		}
		inFile[lower] = true
		out.NameTaken = taken[lower]
		p.channels, p.secrets, p.report = append(p.channels, ch), append(p.secrets, sec), append(p.report, out)
	}
	return p, nil
}

// planChannel checks one channel of a settings file with the secrets the file brought for it, if any.
func planChannel(c backup.Channel, sec alert.Secrets) (store.AlertChannel, alert.Secrets, importedChannel, error) {
	name, err := channelName(c.Name)
	if err != nil {
		return store.AlertChannel{}, nil, importedChannel{}, err
	}
	rules, err := channelRules(c.Rules)
	if err != nil {
		return store.AlertChannel{}, nil, importedChannel{}, err
	}
	settings, err := alert.ParseSettings(c.Settings)
	if err != nil {
		return store.AlertChannel{}, nil, importedChannel{}, fmt.Errorf("settings: %v", err)
	}
	out := importedChannel{Type: c.Type, Name: name, Secrets: "none"}
	complete := true
	for _, field := range c.Secrets {
		if sec[field] == "" {
			complete = false
		}
	}
	switch {
	case len(c.Secrets) == 0:
		settings, err = alert.CheckChannel(c.Type, settings, nil)
	case complete:
		out.Secrets = "included"
		settings, err = alert.CheckChannel(c.Type, settings, sec)
	default: // saved without its secrets, to be entered again
		out.Secrets, sec = "missing", nil
		settings, err = alert.CheckSettings(c.Type, settings)
	}
	if err != nil {
		return store.AlertChannel{}, nil, importedChannel{}, err
	}
	body, err := json.Marshal(settings)
	if err != nil {
		return store.AlertChannel{}, nil, importedChannel{}, err
	}
	out.Enabled = c.Enabled && complete
	return store.AlertChannel{Type: c.Type, Name: name, Settings: body, Rules: rules, Enabled: out.Enabled,
		Tested: out.Enabled}, sec, out, nil
}

// apply returns what store.ImportServices imports of p, leaving out the channels whose names are taken unless
// replace removes the saved ones; nil when the file has no alerts. It reports the channels left out in skipped.
func (p *alertPlan) apply(s *Server, replace bool) *store.AlertImport {
	if p.rules == nil {
		return nil
	}
	a := &store.AlertImport{}
	var secrets []alert.Secrets
	report := []importedChannel{}
	for i, ch := range p.channels {
		if !replace && p.report[i].NameTaken {
			p.skipped = append(p.skipped, skippedService{Type: ch.Type, Name: ch.Name,
				Reason: "an alert channel with this name exists already"})
			continue
		}
		a.Channels, secrets = append(a.Channels, ch), append(secrets, p.secrets[i])
		out := p.report[i]
		out.NameTaken = false
		report = append(report, out)
	}
	p.report = report
	a.Seal = func(i int, id int64) ([]byte, error) { return alert.SealSecrets(s.Box, id, secrets[i]) }
	if replace {
		a.Rules = p.rules // adding keeps the rules in force here
	}
	return a
}
