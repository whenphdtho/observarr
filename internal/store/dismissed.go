package store

import (
	"context"
	"time"
)

// Dismiss records that the suggestion identified by key ("<type> <normalised url>") should not
// be suggested again. It is idempotent: dismissing an already-dismissed key succeeds without
// changing its created_at.
func (s *Store) Dismiss(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO dismissed (key, created_at) VALUES (?, ?) ON CONFLICT (key) DO NOTHING",
		key, time.Now().Unix())
	return err
}

// Dismissed returns every currently dismissed key. It never returns a nil map.
func (s *Store) Dismissed(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key FROM dismissed")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		out[key] = true
	}
	return out, rows.Err()
}
