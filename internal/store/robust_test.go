package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// schema returns the database's schema as SQLite keeps it, one statement per object, in a stable order.
func schema(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query("SELECT type, name, coalesce(sql, '') FROM sqlite_master ORDER BY type, name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, stmt string
		if err := rows.Scan(&typ, &name, &stmt); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s: %s\n", typ, name, stmt)
	}
	return b.String()
}

// TestMigratesFromEveryVersion builds a database at each schema version, from none to the latest, and checks
// that opening it brings it to exactly the schema a new database gets, intact.
func TestMigratesFromEveryVersion(t *testing.T) {
	fresh, _ := openTemp(t)
	want := schema(t, fresh.db)
	latest := migrationCount(t)
	for version := 0; version <= latest; version++ {
		t.Run("from "+strconv.Itoa(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "observarr.db")
			db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
				t.Fatal(err)
			}
			for i := range version {
				body, err := migrationFiles.ReadFile(fmt.Sprintf("migrations/%04d_%s", i+1, migrationSuffix(t, i+1)))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(string(body)); err != nil {
					t.Fatalf("migration %d: %v", i+1, err)
				}
			}
			if _, err := db.Exec("PRAGMA user_version = " + strconv.Itoa(version)); err != nil {
				t.Fatal(err)
			}
			db.Close()

			s, err := Open(context.Background(), path, quiet)
			if err != nil {
				t.Fatalf("open at version %d: %v", version, err)
			}
			defer s.Close()
			var got int
			if err := s.db.QueryRow("PRAGMA user_version").Scan(&got); err != nil || got != latest {
				t.Fatalf("version after opening = %d (%v), want %d", got, err, latest)
			}
			if err := checkIntegrity(context.Background(), s.db); err != nil {
				t.Fatal(err)
			}
			if got := schema(t, s.db); got != want {
				t.Fatalf("schema after migrating from %d differs:\n%s\nwant:\n%s", version, got, want)
			}
			if err := s.Probe(context.Background()); err != nil {
				t.Fatalf("a migrated database takes no writes: %v", err)
			}
		})
	}
}

// migrationSuffix returns the part of migration n's file name after its number.
func migrationSuffix(t *testing.T, n int) string {
	t.Helper()
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("%04d_", n)
	for _, e := range entries {
		if rest, ok := strings.CutPrefix(e.Name(), prefix); ok {
			return rest
		}
	}
	t.Fatalf("no migration %d", n)
	return ""
}

func noKey(int64) ([]byte, error) { return nil, nil }

// TestReadOnlyDatabaseStillAnswersReads makes the database refuse writes, as on a full disk or a file system
// mounted read-only: every write fails with an error Degraded explains, and reads go on working.
func TestReadOnlyDatabaseStillAnswersReads(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.CreateService(ctx, Service{Type: "t", Name: "A", BaseURL: "http://a"}, noKey); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA query_only = 1"); err != nil {
		t.Fatal(err)
	}
	for name, write := range map[string]func() error{
		"probe": func() error { return s.Probe(ctx) },
		"service": func() error {
			_, err := s.CreateService(ctx, Service{Type: "t", Name: "B", BaseURL: "http://b"}, noKey)
			return err
		},
		"history": func() error { return s.AddHistory(ctx, minute(time.Now().Unix()/60*60, SourceBytes{"host", 1, 1})) },
		"traffic": func() error { return s.AddTraffic(ctx, []TrafficRow{{Day: "2026-11-26", Scope: "host", Rx: 1}}) },
	} {
		if err := write(); Degraded(err) != "database is read-only" {
			t.Errorf("%s write on a read-only database = %v, want a read-only error", name, err)
		}
	}
	if list, err := s.Services(ctx); err != nil || len(list) != 1 {
		t.Fatalf("Services on a read-only database = %v, %v", list, err)
	}
}

// TestLockedDatabaseStillAnswersReads holds the write lock from another connection, as a backup tool or a
// second process could: a write waits out the busy timeout and then fails, reads are not held up, and writes
// work again once the lock is gone.
func TestLockedDatabaseStillAnswersReads(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	if _, err := s.db.Exec("PRAGMA busy_timeout = 100"); err != nil { // the one connection; the default is 5 s
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	err = s.Probe(ctx)
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
		t.Fatalf("write while locked = %v, want SQLITE_BUSY", err)
	}
	if took := time.Since(start); took < 100*time.Millisecond {
		t.Fatalf("write gave up after %v, before the busy timeout", took)
	}
	if _, err := s.Services(ctx); err != nil {
		t.Fatalf("read while locked: %v", err)
	}

	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(ctx); err != nil {
		t.Fatalf("write after the lock was released: %v", err)
	}
}

// TestHistoryWritesUnderLoad writes minutes of history from many goroutines while others read: the single
// connection serialises them, so none fails as busy and every minute is kept.
func TestHistoryWritesUnderLoad(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC).Unix()
	const writers, minutes = 8, 30
	var wg sync.WaitGroup
	errs := make(chan error, writers*minutes*2)
	for w := range writers {
		wg.Go(func() {
			for m := range minutes {
				ts := base + int64(w*minutes+m)*60
				errs <- s.AddHistory(ctx, minute(ts, SourceBytes{"host", 1, 1}, SourceBytes{"container:a", 1, 0}))
			}
		})
		wg.Go(func() {
			for range minutes {
				_, err := s.TrafficSeries(ctx, ResMinute, base, base+writers*minutes*60)
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	rx, _, ok, err := s.HostTraffic(ctx, ResMinute, base, base+writers*minutes*60)
	if err != nil || !ok || rx != writers*minutes {
		t.Fatalf("host traffic = %d %v %v, want %d", rx, ok, err, writers*minutes)
	}
}

// TestWALStaysBounded writes one large transaction and then a small one: once checkpointed, the write-ahead log
// is cut back to journalSizeLimit instead of keeping the size of the largest transaction ever written.
func TestWALStaysBounded(t *testing.T) {
	s, path := openTemp(t)
	if _, err := s.db.Exec("CREATE TABLE filler (b BLOB)"); err != nil {
		t.Fatal(err)
	}
	// 8 MiB in one transaction, twice the limit.
	if _, err := s.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < 8)
		INSERT INTO filler SELECT randomblob(1 << 20) FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(context.Background()); err != nil { // the next write starts the log over and cuts it
		t.Fatal(err)
	}
	fi, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > journalSizeLimit {
		t.Fatalf("write-ahead log is %d MiB after a checkpoint", fi.Size()>>20)
	}
}
