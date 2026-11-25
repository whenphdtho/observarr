package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Service is one monitored app as stored.
type Service struct {
	ID        int64
	Type      string
	Name      string
	BaseURL   string
	SealedKey []byte // nil when the app needs no key
	Container string // linked Docker container name; "" = none
}

// CreateService inserts svc, then calls seal with the new row id and stores whatever it returns
// as the sealed key — insert and seal happen in one transaction, so the key is always bound to
// the id it was sealed for, and a failing seal leaves no row behind. It returns ErrExists if
// svc.Name is already taken (names are unique case-insensitively).
func (s *Store) CreateService(ctx context.Context, svc Service, seal func(id int64) ([]byte, error)) (Service, error) {
	var created Service
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		created, err = createService(ctx, tx, svc, seal)
		return err
	})
	if err != nil {
		return Service{}, err
	}
	return created, nil
}

// ImportServices adds svcs, each sealed as CreateService does (seal gets the index in svcs and the new row id),
// and records dismissed as Dismiss does, all in one transaction: on any error nothing changes. With replace,
// every saved service is removed first. It returns the added services, in the order of svcs, or ErrExists
// if a name is taken.
func (s *Store) ImportServices(ctx context.Context, replace bool, svcs []Service, dismissed []string,
	seal func(i int, id int64) ([]byte, error)) ([]Service, error) {
	var created []Service
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if replace {
			if _, err := tx.ExecContext(ctx, "DELETE FROM services"); err != nil {
				return err
			}
		}
		created = make([]Service, 0, len(svcs))
		for i, svc := range svcs {
			c, err := createService(ctx, tx, svc, func(id int64) ([]byte, error) { return seal(i, id) })
			if err != nil {
				return err
			}
			created = append(created, c)
		}
		now := time.Now().Unix()
		for _, key := range dismissed {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO dismissed (key, created_at) VALUES (?, ?) ON CONFLICT (key) DO NOTHING", key, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// createService inserts svc in tx and seals its key for the new row id; see CreateService.
func createService(ctx context.Context, tx *sql.Tx, svc Service, seal func(id int64) ([]byte, error)) (Service, error) {
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx,
		"INSERT INTO services (type, name, base_url, api_key, container, created_at, updated_at) VALUES (?, ?, ?, NULL, ?, ?, ?)",
		svc.Type, svc.Name, svc.BaseURL, svc.Container, now, now)
	if err != nil {
		if isUniqueViolation(err) {
			return Service{}, ErrExists
		}
		return Service{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Service{}, err
	}
	sealed, err := seal(id)
	if err != nil {
		return Service{}, fmt.Errorf("seal service key: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE services SET api_key = ? WHERE id = ?", sealed, id); err != nil {
		return Service{}, err
	}
	return Service{ID: id, Type: svc.Type, Name: svc.Name, BaseURL: svc.BaseURL, SealedKey: sealed,
		Container: svc.Container}, nil
}

// Services returns every service, ordered by name. It never returns a nil slice.
func (s *Store) Services(ctx context.Context) ([]Service, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT id, type, name, base_url, api_key, container FROM services ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Service{}
	for rows.Next() {
		var svc Service
		if err := rows.Scan(&svc.ID, &svc.Type, &svc.Name, &svc.BaseURL, &svc.SealedKey, &svc.Container); err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, rows.Err()
}

// ServiceByID returns the service with id, or ErrNotFound.
func (s *Store) ServiceByID(ctx context.Context, id int64) (Service, error) {
	var svc Service
	err := s.db.QueryRowContext(ctx, "SELECT id, type, name, base_url, api_key, container FROM services WHERE id = ?", id).
		Scan(&svc.ID, &svc.Type, &svc.Name, &svc.BaseURL, &svc.SealedKey, &svc.Container)
	if errors.Is(err, sql.ErrNoRows) {
		return Service{}, ErrNotFound
	}
	return svc, err
}

// UpdateService updates the name, base_url, api_key and container of the service identified by svc.ID
// (svc.Type is never changed). It returns ErrNotFound if there is no such service, or ErrExists
// if svc.Name collides with another service.
func (s *Store) UpdateService(ctx context.Context, svc Service) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE services SET name = ?, base_url = ?, api_key = ?, container = ?, updated_at = ? WHERE id = ?",
		svc.Name, svc.BaseURL, svc.SealedKey, svc.Container, time.Now().Unix(), svc.ID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteService removes the service identified by id. It returns ErrNotFound if there is no such
// service.
func (s *Store) DeleteService(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM services WHERE id = ?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
