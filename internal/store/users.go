package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// User is an account that can use Observarr.
type User struct {
	ID           int64
	Name         string
	PasswordHash string
}

// HasUsers reports whether any account exists (false = first-start setup is open).
func (s *Store) HasUsers(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM users)").Scan(&exists)
	return exists, err
}

// CreateFirstUser creates the admin account only while no account exists, in one statement, so two
// concurrent setup requests cannot both succeed. It returns ErrExists once setup is done.
func (s *Store) CreateFirstUser(ctx context.Context, name, passwordHash string) (User, error) {
	res, err := s.db.ExecContext(ctx,
		"INSERT INTO users (name, password_hash, created_at) SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)",
		name, passwordHash, time.Now().Unix())
	if err != nil {
		return User{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return User{}, err
	}
	if n == 0 {
		return User{}, ErrExists
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Name: name, PasswordHash: passwordHash}, nil
}

// EnsureUser returns the account called name, creating it without a usable password if it does not exist.
// Used for users asserted by a trusted login proxy.
func (s *Store) EnsureUser(ctx context.Context, name string) (User, error) {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO users (name, password_hash, created_at) VALUES (?, '!', ?) ON CONFLICT (name) DO NOTHING",
		name, time.Now().Unix())
	if err != nil {
		return User{}, err
	}
	return s.UserByName(ctx, name)
}

// UserByName looks an account up by name (case-insensitive). It returns ErrNotFound if there is none.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, "SELECT id, name, password_hash FROM users WHERE name = ?", name).
		Scan(&u.ID, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}
