package store

import (
	"context"
	"strings"
	"time"
)

// AuditRow is one audit entry.
type AuditRow struct {
	ID     int64
	At     time.Time
	Actor  string
	Action string
	Detail string
}

// AuditRows returns the newest audit rows, at most limit, whose action is one of actions and which were written
// at or after since, newest first.
func (s *Store) AuditRows(ctx context.Context, actions []string, since time.Time, limit int) ([]AuditRow, error) {
	if len(actions) == 0 || limit <= 0 {
		return []AuditRow{}, nil
	}
	args := make([]any, 0, len(actions)+2)
	for _, a := range actions {
		args = append(args, a)
	}
	args = append(args, since.Unix(), limit)
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, actor, action, detail FROM audit
		WHERE action IN (?`+strings.Repeat(", ?", len(actions)-1)+`) AND ts >= ? ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRow{}
	for rows.Next() {
		var r AuditRow
		var ts int64
		if err := rows.Scan(&r.ID, &ts, &r.Actor, &r.Action, &r.Detail); err != nil {
			return nil, err
		}
		r.At = time.Unix(ts, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionRow is one unexpired session as a list shows it. TokenHash identifies it; it is never shown.
type SessionRow struct {
	TokenHash []byte
	UserName  string
	Expires   time.Time
}

// ListSessions returns the sessions unexpired at now, at most limit, the latest to expire (the newest) first.
func (s *Store) ListSessions(ctx context.Context, now time.Time, limit int) ([]SessionRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.token_hash, u.name, s.expires_at FROM sessions s
		JOIN users u ON u.id = s.user_id WHERE s.expires_at > ? ORDER BY s.expires_at DESC LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionRow{}
	for rows.Next() {
		var r SessionRow
		var exp int64
		if err := rows.Scan(&r.TokenHash, &r.UserName, &exp); err != nil {
			return nil, err
		}
		r.Expires = time.Unix(exp, 0)
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountSessions returns how many sessions are unexpired at now.
func (s *Store) CountSessions(ctx context.Context, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE expires_at > ?", now.Unix()).Scan(&n)
	return n, err
}

// DeleteSessionsExcept ends every session unexpired at now but the one identified by keep, of every user, and
// returns how many it ended. Expired ones are left to DeleteExpiredSessions.
func (s *Store) DeleteSessionsExcept(ctx context.Context, keep []byte, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash != ? AND expires_at > ?", keep, now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
