package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	auditMaxAge        = 90 * 24 * time.Hour // older audit rows are removed
	maxAuditRows       = 100_000             // beyond this, the oldest audit rows are removed
	maxSessionsPerUser = 50                  // beyond this, a user's oldest sessions end
)

// Probe writes one row to check that the database takes writes. Degraded explains a failure.
func (s *Store) Probe(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO probe (id, at) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET at = excluded.at", time.Now().Unix())
	return err
}

// Degraded returns "database is read-only" or "database is full" when err means the database cannot take
// writes at all, and "" for any other error.
func Degraded(err error) string {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return ""
	}
	switch sqliteErr.Code() & 0xff {
	case sqlite3.SQLITE_READONLY:
		return "database is read-only"
	case sqlite3.SQLITE_FULL:
		return "database is full"
	}
	return ""
}

// HasSealedKeys reports whether any service has a saved (sealed) API key, or any alert channel sealed secrets.
func (s *Store) HasSealedKeys(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM services WHERE api_key IS NOT NULL)
		OR EXISTS (SELECT 1 FROM alert_channels WHERE secrets IS NOT NULL)`).Scan(&exists)
	return exists, err
}

// ClearSealedKeys removes every saved API key and alert channel secret, for when the key that sealed them is
// lost, and returns how many services and channels had one. A channel that lost its secrets is disabled until it
// is tested again.
func (s *Store) ClearSealedKeys(ctx context.Context) (int64, error) {
	var n int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{"UPDATE services SET api_key = NULL WHERE api_key IS NOT NULL",
			"UPDATE alert_channels SET secrets = NULL, enabled = 0, tested = 0 WHERE secrets IS NOT NULL"} {
			res, err := tx.ExecContext(ctx, q)
			if err != nil {
				return err
			}
			rows, err := res.RowsAffected()
			if err != nil {
				return err
			}
			n += rows
		}
		return nil
	})
	return n, err
}

// PruneAudit removes audit rows older than auditMaxAge and, beyond maxAuditRows, the oldest ones.
func (s *Store) PruneAudit(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM audit WHERE ts < ?", now.Add(-auditMaxAge).Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM audit WHERE id <= (SELECT id FROM audit ORDER BY id DESC LIMIT 1 OFFSET ?)", maxAuditRows)
	return err
}
