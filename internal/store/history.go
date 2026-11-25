package store

import (
	"context"
	"database/sql"
	"time"
)

// Resolutions of the history buckets, in seconds. A day bucket is a local day, so it can last 23 or 25 hours.
const (
	ResMinute = 60
	ResHour   = 3600
	ResDay    = 86400
)

// How long each resolution's buckets are kept: minutes for two days (a day and the day before it), hours for
// 35 days (a week and the week before it, and a month), days for 400 (a year).
const (
	MinutesKeep = 48 * time.Hour
	HoursKeep   = 35 * 24 * time.Hour
	DaysKeep    = 400 * 24 * time.Hour
)

const (
	// MaxSources is how many traffic rows a bucket holds at most: the host, the containers and OtherSource.
	MaxSources = 64
	// OtherSource sums the containers beyond the first MaxSources-2 of a bucket.
	OtherSource = "other"
	hostSource  = "host"
)

// SourceBytes is one source's traffic in a bucket, in bytes. Source is "host", "container:<name>" or
// OtherSource.
type SourceBytes struct {
	Source string
	Rx, Tx uint64
}

// HostAvg is the host's average CPU use (percent of all CPUs) and memory used (bytes) over a minute.
type HostAvg struct {
	CPU float64
	Mem uint64
}

// HistoryMinute is one minute's figures, with the starts of the minute, of its hour and of its local day
// (seconds since the epoch). Traffic holds the host first and the containers by most traffic; beyond the
// bucket's MaxSources the containers are summed into OtherSource. Host is nil when the host was not read.
type HistoryMinute struct {
	Minute, Hour, Day int64
	Traffic           []SourceBytes
	Host              *HostAvg
}

// CapSources keeps the host, the first MaxSources-2 containers and the rest summed into OtherSource. rows has
// the host first (if at all) and the containers by most traffic.
func CapSources(rows []SourceBytes) []SourceBytes {
	if len(rows) <= MaxSources-1 {
		return rows
	}
	out := make([]SourceBytes, 0, MaxSources)
	other := SourceBytes{Source: OtherSource}
	named := 0
	for _, r := range rows {
		if r.Source == hostSource || (r.Source != OtherSource && named < MaxSources-2) {
			if r.Source != hostSource {
				named++
			}
			out = append(out, r)
		} else {
			other.Rx += r.Rx
			other.Tx += r.Tx
		}
	}
	return append(out, other)
}

// AddHistory adds one minute to its minute, hour and day buckets, in one transaction. A bucket keeps at most
// MaxSources traffic rows: a container it has no room for counts as OtherSource.
func (s *Store) AddHistory(ctx context.Context, m HistoryMinute) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		add, err := tx.PrepareContext(ctx, `INSERT INTO traffic_series (res, ts, source, rx, tx, n)
			VALUES (?, ?, ?, ?, ?, 1) ON CONFLICT (res, ts, source)
			DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx, n = n + 1`)
		if err != nil {
			return err
		}
		defer add.Close()
		host, err := tx.PrepareContext(ctx, `INSERT INTO host_series (res, ts, cpu, mem, n) VALUES (?, ?, ?, ?, 1)
			ON CONFLICT (res, ts) DO UPDATE SET cpu = (cpu * n + excluded.cpu) / (n + 1),
			mem = (mem * n + excluded.mem) / (n + 1), n = n + 1`)
		if err != nil {
			return err
		}
		defer host.Close()
		for _, b := range [...]struct{ res, ts int64 }{{ResMinute, m.Minute}, {ResHour, m.Hour}, {ResDay, m.Day}} {
			rows, err := admit(ctx, tx, b.res, b.ts, m.Traffic)
			if err != nil {
				return err
			}
			for _, r := range rows {
				if _, err := add.ExecContext(ctx, b.res, b.ts, r.Source, clamp(r.Rx), clamp(r.Tx)); err != nil {
					return err
				}
			}
			if m.Host != nil {
				if _, err := host.ExecContext(ctx, b.res, b.ts, m.Host.CPU, clamp(m.Host.Mem)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// admit returns rows as the bucket takes them: the host and the containers it already holds, new containers
// while it has room for them, and the rest summed into OtherSource.
func admit(ctx context.Context, tx *sql.Tx, res, ts int64, rows []SourceBytes) ([]SourceBytes, error) {
	q, err := tx.QueryContext(ctx, "SELECT source FROM traffic_series WHERE res = ? AND ts = ?", res, ts)
	if err != nil {
		return nil, err
	}
	defer q.Close()
	held, named := map[string]bool{}, 0
	for q.Next() {
		var src string
		if err := q.Scan(&src); err != nil {
			return nil, err
		}
		held[src] = true
		if src != hostSource && src != OtherSource {
			named++
		}
	}
	if err := q.Err(); err != nil {
		return nil, err
	}
	out := make([]SourceBytes, 0, len(rows))
	other := SourceBytes{Source: OtherSource}
	for _, r := range rows {
		switch {
		case r.Source == hostSource || held[r.Source]:
			out = append(out, r)
		case r.Source != OtherSource && named < MaxSources-2:
			out = append(out, r)
			named++
		default:
			other.Rx += r.Rx
			other.Tx += r.Tx
		}
	}
	if other.Rx > 0 || other.Tx > 0 {
		out = append(out, other)
	}
	return out, nil
}

// SeriesRow is one source's traffic in one bucket; N is how many minutes it holds.
type SeriesRow struct {
	TS     int64
	Source string
	Rx, Tx uint64
	N      int64
}

// TrafficSeries returns the traffic rows of the res buckets that start in [from, to), oldest first.
func (s *Store) TrafficSeries(ctx context.Context, res, from, to int64) ([]SeriesRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, source, rx, tx, n FROM traffic_series
		WHERE res = ? AND ts >= ? AND ts < ? ORDER BY ts`, res, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesRow
	for rows.Next() {
		var r SeriesRow
		var rx, tx int64
		if err := rows.Scan(&r.TS, &r.Source, &rx, &tx, &r.N); err != nil {
			return nil, err
		}
		r.Rx, r.Tx = uint64(max(rx, 0)), uint64(max(tx, 0))
		out = append(out, r)
	}
	return out, rows.Err()
}

// HostRow is the host's averages in one bucket; N is how many minutes they cover.
type HostRow struct {
	TS  int64
	CPU float64
	Mem uint64
	N   int64
}

// HostSeries returns the host rows of the res buckets that start in [from, to), oldest first.
func (s *Store) HostSeries(ctx context.Context, res, from, to int64) ([]HostRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ts, cpu, mem, n FROM host_series
		WHERE res = ? AND ts >= ? AND ts < ? ORDER BY ts`, res, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostRow
	for rows.Next() {
		var r HostRow
		var mem int64
		if err := rows.Scan(&r.TS, &r.CPU, &mem, &r.N); err != nil {
			return nil, err
		}
		r.Mem = uint64(max(mem, 0))
		out = append(out, r)
	}
	return out, rows.Err()
}

// HostTraffic returns the host's bytes in the res buckets that start in [from, to). ok is false when that
// period is not covered: there is no row in it, or the oldest res bucket kept starts more than two buckets
// after from, so the sum would hold only part of the period.
func (s *Store) HostTraffic(ctx context.Context, res, from, to int64) (rx, tx uint64, ok bool, err error) {
	var oldest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, "SELECT min(ts) FROM traffic_series WHERE res = ?", res).
		Scan(&oldest); err != nil || !oldest.Valid || oldest.Int64 > from+2*res {
		return 0, 0, false, err
	}
	var n, r, t int64
	err = s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(rx), 0), coalesce(sum(tx), 0)
		FROM traffic_series WHERE res = ? AND ts >= ? AND ts < ? AND source = ?`, res, from, to, hostSource).
		Scan(&n, &r, &t)
	return uint64(max(r, 0)), uint64(max(t, 0)), err == nil && n > 0, err
}

// TrafficDays returns scope's daily totals (see AddTraffic) on the days from from up to but not including to,
// by day.
func (s *Store) TrafficDays(ctx context.Context, scope, from, to string) (map[string]TrafficRow, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT day, rx, tx FROM traffic_daily WHERE scope = ? AND day >= ? AND day < ?", scope, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]TrafficRow{}
	for rows.Next() {
		r := TrafficRow{Scope: scope}
		var rx, tx int64
		if err := rows.Scan(&r.Day, &rx, &tx); err != nil {
			return nil, err
		}
		r.Rx, r.Tx = uint64(max(rx, 0)), uint64(max(tx, 0))
		out[r.Day] = r
	}
	return out, rows.Err()
}

// PruneHistory removes minute buckets older than MinutesKeep, hour buckets older than HoursKeep and day
// buckets older than DaysKeep.
func (s *Store) PruneHistory(ctx context.Context, now time.Time) error {
	for _, table := range []string{"traffic_series", "host_series"} {
		for _, k := range [...]struct {
			res  int64
			keep time.Duration
		}{{ResMinute, MinutesKeep}, {ResHour, HoursKeep}, {ResDay, DaysKeep}} {
			if _, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE res = ? AND ts < ?",
				k.res, now.Add(-k.keep).Unix()); err != nil {
				return err
			}
		}
	}
	return nil
}
