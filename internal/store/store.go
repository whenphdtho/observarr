// Package store keeps Observarr's state in one SQLite file (WAL mode).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when a row that must be unique already exists.
var ErrExists = errors.New("already exists")

// Store wraps the database.
type Store struct {
	db   *sql.DB
	path string
}

// ErrCorrupt is returned by Open when the database file is damaged. Open never replaces such a file; see MoveAside.
var ErrCorrupt = errors.New("database is corrupt")

// Open opens or creates the database at path and brings its schema up to date. A database that fails its
// integrity check is left untouched and ErrCorrupt returned. An existing database is backed up before any
// migration runs.
func Open(ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	_, statErr := os.Stat(path)
	existed := statErr == nil

	db, err := openDB(ctx, path)
	if existed && err == nil {
		err = checkIntegrity(ctx, db)
	}
	if err != nil {
		if db != nil {
			db.Close()
		}
		if existed && isCorrupt(err) {
			return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
		}
		return nil, err
	}

	s := &Store{db: db, path: path}
	if err := s.migrate(ctx, existed, log); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// journalSizeLimit is what the write-ahead log is cut back to once checkpointed; without it, the log keeps the
// size of the largest transaction ever written (a migration, a big prune).
const journalSizeLimit = 4 << 20

func openDB(ctx context.Context, path string) (*sql.DB, error) {
	pragmas := "_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_size_limit(" + strconv.Itoa(journalSizeLimit) + ")"
	dsn := (&url.URL{Scheme: "file", OmitHost: true, Path: path, RawQuery: pragmas}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // a single connection: no lock contention between our own writers
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	return db, nil
}

// errIntegrity reports that PRAGMA quick_check found damage; its message is quick_check's own result.
type errIntegrity string

func (e errIntegrity) Error() string { return string(e) }

func checkIntegrity(ctx context.Context, db *sql.DB) error {
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errIntegrity(result)
	}
	return nil
}

// isCorrupt reports whether err means the database file itself is damaged (quick_check found
// damage, or the driver reports SQLITE_CORRUPT/SQLITE_NOTADB) as opposed to a transient or
// environmental problem such as a permission error, a lock, or a cancelled context — those must
// be returned to the caller unchanged instead of destroying a healthy database file.
func isCorrupt(err error) bool {
	var integrity errIntegrity
	if errors.As(err, &integrity) {
		return true
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		switch sqliteErr.Code() & 0xff {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return true
		}
	}
	return false
}

// isUniqueViolation reports whether err is a UNIQUE constraint failure, so callers can turn it
// into ErrExists instead of leaking a driver-specific error.
func isUniqueViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

// MoveAside renames the database (and its WAL/SHM files when present, renamed first so a fresh
// database later created at path can never pick up a stale WAL) to <path>.corrupt-<time>, readable by the
// owner only, and returns the new path.
func MoveAside(path string) (string, error) {
	moved := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Rename(path+suffix, moved+suffix); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	if err := os.Rename(path, moved); err != nil {
		return "", err
	}
	return moved, os.Chmod(moved, 0o600)
}

// migrate applies the embedded migrations in order. Each runs in its own transaction together with the
// schema version bump, so a failure leaves the database at the previous version.
func (s *Store) migrate(ctx context.Context, existed bool, log *slog.Logger) error {
	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	latest := len(names)

	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current > latest {
		return fmt.Errorf("database schema %d is newer than this Observarr (knows %d): run the newer version or restore a backup", current, latest)
	}
	if current == latest {
		return nil
	}
	if existed {
		dst, err := s.Backup(ctx, "pre-migrate-"+strconv.Itoa(current))
		if err != nil {
			return fmt.Errorf("backup before migration: %w", err)
		}
		log.Info("backed up database before migrating", "file", dst)
	}
	for i := current; i < latest; i++ {
		body, err := migrationFiles.ReadFile(names[i])
		if err != nil {
			return err
		}
		err = s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(body)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(i+1))
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s: %w", names[i], err)
		}
		log.Info("applied migration", "file", names[i])
	}
	return nil
}

func (s *Store) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// maxBackups is how many backups Backup keeps; older ones are removed.
const maxBackups = 5

// Backup writes a consistent copy of the database to <data dir>/backups (0600) and returns its path. The copy
// is written under a temporary name and renamed when complete, so a backup file is never partial. Only the
// newest maxBackups backups are kept. Temporary files a crash left behind are removed first: the data directory
// lock means no other process can be writing them.
func (s *Store) Backup(ctx context.Context, label string) (string, error) {
	dir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stale, _ := filepath.Glob(filepath.Join(dir, "observarr-*.db.tmp*")) // with SQLite's -journal
	for _, f := range stale {
		os.Remove(f)
	}
	dst := filepath.Join(dir, fmt.Sprintf("observarr-%s-%s.db", time.Now().UTC().Format("20060102T150405.000000000Z"), label))
	tmp := dst + ".tmp"
	_, err := s.db.ExecContext(ctx, "VACUUM INTO ?", tmp)
	if err == nil {
		err = os.Chmod(tmp, 0o600)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("write backup %s: %w", dst, err)
	}
	// Names start with the time, so they sort oldest first. Removing is best effort: the new backup exists.
	old, _ := filepath.Glob(filepath.Join(dir, "observarr-*.db"))
	sort.Strings(old)
	for _, f := range old[:max(0, len(old)-maxBackups)] {
		os.Remove(f)
	}
	return dst, nil
}
