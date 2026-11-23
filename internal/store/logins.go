package store

import (
	"context"
	"database/sql"
	"time"
)

// maxKnownLogins bounds the addresses remembered by RememberLogin; the oldest login is forgotten first.
const maxKnownLogins = 1000

// RememberLogin records a successful login from key, a client address (see auth.LimitKey), at at.
func (s *Store) RememberLogin(ctx context.Context, key string, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO login_known (key, at) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET at = excluded.at`, key, at.Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM login_known WHERE key NOT IN
			(SELECT key FROM login_known ORDER BY at DESC LIMIT ?)`, maxKnownLogins)
		return err
	})
}

// KnownLogins returns the time of each remembered address's latest login, for logins since since.
func (s *Store) KnownLogins(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, at FROM login_known WHERE at >= ?", since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	known := make(map[string]time.Time)
	for rows.Next() {
		var key string
		var at int64
		if err := rows.Scan(&key, &at); err != nil {
			return nil, err
		}
		known[key] = time.Unix(at, 0)
	}
	return known, rows.Err()
}

// PruneKnownLogins forgets addresses whose latest login was before cutoff.
func (s *Store) PruneKnownLogins(ctx context.Context, cutoff time.Time) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM login_known WHERE at < ?", cutoff.Unix())
	return err
}
