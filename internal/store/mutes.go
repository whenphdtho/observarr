package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// AlertMute silences the alert of one condition, by its key, until Until.
type AlertMute struct {
	Key       string
	Until     time.Time
	CreatedBy string
}

// MaxAlertMutes bounds the mutes kept at once.
const MaxAlertMutes = 500

// ErrTooMany is returned when a bounded table is full.
var ErrTooMany = errors.New("too many")

// AlertMutes returns every mute, ended ones included (PruneAlertMutes removes those).
func (s *Store) AlertMutes(ctx context.Context) ([]AlertMute, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, until, created_by FROM alert_mutes ORDER BY key")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertMute
	for rows.Next() {
		var m AlertMute
		var until int64
		if err := rows.Scan(&m.Key, &until, &m.CreatedBy); err != nil {
			return nil, err
		}
		m.Until = time.Unix(until, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetAlertMute saves m, replacing a mute of the same key. A new key beyond MaxAlertMutes is ErrTooMany.
func (s *Store) SetAlertMute(ctx context.Context, m AlertMute) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM alert_mutes WHERE key <> ?", m.Key).Scan(&n); err != nil {
			return err
		}
		if n >= MaxAlertMutes {
			return ErrTooMany
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO alert_mutes (key, until, created_by) VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET until = excluded.until, created_by = excluded.created_by`,
			m.Key, m.Until.Unix(), m.CreatedBy)
		return err
	})
}

// DeleteAlertMutes removes the mutes of keys; a key without one is left alone.
func (s *Store) DeleteAlertMutes(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, key := range keys {
			if _, err := tx.ExecContext(ctx, "DELETE FROM alert_mutes WHERE key = ?", key); err != nil {
				return err
			}
		}
		return nil
	})
}

// PruneAlertMutes removes the mutes that ended by now.
func (s *Store) PruneAlertMutes(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM alert_mutes WHERE until <= ?", now.Unix())
	return err
}
