package store

import (
	"context"
	"time"
)

// Audit records who did what (logins, setup, and later every action and approval) and returns the row's id.
func (s *Store) Audit(ctx context.Context, actor, action, detail string) (int64, error) {
	res, err := s.db.ExecContext(ctx, "INSERT INTO audit (ts, actor, action, detail) VALUES (?, ?, ?, ?)",
		time.Now().Unix(), actor, action, detail)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetAuditDetail replaces the detail of the audit row id, for a row that counts repeated events.
func (s *Store) SetAuditDetail(ctx context.Context, id int64, detail string) error {
	_, err := s.db.ExecContext(ctx, "UPDATE audit SET detail = ? WHERE id = ?", detail, id)
	return err
}
