package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is a logged-in browser session.
type Session struct {
	UserID   int64
	UserName string
	CSRF     string
}

// CreateSession stores a session identified by the hash of its cookie token. A user keeps at most
// maxSessionsPerUser sessions; beyond that the ones expiring first end.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID int64, csrf string, expires time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO sessions (token_hash, user_id, csrf, expires_at) VALUES (?, ?, ?, ?)",
			tokenHash, userID, csrf, expires.Unix()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash NOT IN
			(SELECT token_hash FROM sessions WHERE user_id = ? ORDER BY expires_at DESC LIMIT ?)`,
			userID, userID, maxSessionsPerUser)
		return err
	})
}

// SessionByToken returns the unexpired session for tokenHash, or ErrNotFound.
func (s *Store) SessionByToken(ctx context.Context, tokenHash []byte, now time.Time) (Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx, `
		SELECT s.user_id, u.name, s.csrf FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, now.Unix()).
		Scan(&sess.UserID, &sess.UserName, &sess.CSRF)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sess, err
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", tokenHash)
	return err
}

// DeleteExpiredSessions removes sessions that expired before now and returns how many were removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", now.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
