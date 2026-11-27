package alert

import (
	"context"
	"errors"
	"slices"
	"time"

	"observarr/internal/store"
)

// A mute silences one condition, by its key, for a while: it sends nothing and the UI does not count it, but it
// stays listed. It ends at its time, when the condition is over (no "resolved" is sent then), or when the
// condition gets worse than it was when muted. Mutes are saved, so a restart keeps them.

// MuteSpans are the spans a mute may last.
var MuteSpans = []time.Duration{time.Hour, 8 * time.Hour, 24 * time.Hour}

// ErrNotListed is returned for a key that nothing in the attention list has.
var ErrNotListed = errors.New("alert: nothing that needs attention has this key")

// mute is one mute as the Service follows it.
type mute struct {
	until time.Time
	sev   Severity // the condition's severity when it was muted, saved with it: a worse one breaks the mute
	// seen tells whether the condition was evaluated since the mute was set or loaded, and scope is its scope
	// then. A mute loaded at a start is not seen yet: its condition is read again first.
	seen  bool
	scope string
}

// loadMutes reads the saved mutes that have not ended.
func (s *Service) loadMutes(ctx context.Context) error {
	rows, err := s.cfg.Store.AlertMutes(ctx)
	if err != nil {
		return err
	}
	now := s.cfg.Now()
	for _, r := range rows {
		if r.Until.After(now) {
			s.mutes[r.Key] = &mute{until: r.Until, sev: Severity(r.Severity)}
		}
	}
	return nil
}

// Mute silences the condition with key for span, in the name of the user by, and returns when the mute ends. The
// key must be listed now (ErrNotListed), and at most store.MaxAlertMutes are kept (store.ErrTooMany). A condition
// whose rule is switched off can be muted too: it sends nothing either way, and the mute just ends.
func (s *Service) Mute(ctx context.Context, key string, span time.Duration, by string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.conds, func(c Condition) bool { return c.Key == key })
	if i < 0 {
		return time.Time{}, ErrNotListed
	}
	// Whole seconds, as they are saved: the time is the same after a restart.
	until := s.cfg.Now().Add(span).Truncate(time.Second)
	if err := s.cfg.Store.SetAlertMute(ctx, store.AlertMute{Key: key, Until: until, Severity: string(s.conds[i].Severity),
		CreatedBy: by}); err != nil {
		return time.Time{}, err
	}
	s.mutes[key] = &mute{until: until, seen: true, sev: s.conds[i].Severity, scope: s.conds[i].Scope}
	s.setAttention(s.conds)
	return until, nil
}

// Unmute ends the mute of key; without one it does nothing.
func (s *Service) Unmute(ctx context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.mutes[key]; !ok {
		return nil
	}
	if err := s.cfg.Store.DeleteAlertMutes(ctx, key); err != nil {
		return err
	}
	delete(s.mutes, key)
	s.setAttention(s.conds)
	return nil
}

// settleMutes ends the mutes that are due at now, given the conditions and the scopes just read: one whose time
// has come, one whose condition got worse (the engine then sends it as it would any worse alert), and one whose
// condition is over, which the engine forgets without a word. It returns the keys of the mutes ended, to remove
// from the store, and of the firing alerts forgotten. Callers hold s.mu.
func (s *Service) settleMutes(now time.Time, conds []Condition, known map[string]bool) (ended, forgotten []string) {
	byKey := make(map[string]*Condition, len(conds))
	for i := range conds {
		byKey[conds[i].Key] = &conds[i]
	}
	for key, m := range s.mutes {
		c := byKey[key]
		switch {
		case !now.Before(m.until):
		case c != nil && rank(c.Severity) < rank(m.sev):
		case c != nil:
			m.seen, m.scope = true, c.Scope
			continue
		// Gone: over if what it was read from was read again without it. After a start that is not known yet;
		// once the data has come in, a mute whose condition never showed is over too.
		case m.seen && known[m.scope], !m.seen && now.Sub(s.started) >= warmUp:
			if s.engine.Forget(key) {
				forgotten = append(forgotten, key)
			}
		default:
			continue
		}
		delete(s.mutes, key)
		ended = append(ended, key)
	}
	return ended, forgotten
}
