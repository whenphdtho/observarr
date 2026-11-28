package store

import (
	"context"
	"database/sql"
	"math"
	"time"
)

// TrafficKeepDays is how many days of traffic totals are kept: enough for a monthly budget.
const TrafficKeepDays = 35

// TrafficRow is one scope's traffic on one local day, in bytes. Day is "YYYY-MM-DD"; Scope is "host" or
// "container:<name>".
type TrafficRow struct {
	Day, Scope string
	Rx, Tx     uint64
}

// AddTraffic adds each row's bytes to that day's and scope's totals, in one transaction.
func (s *Store) AddTraffic(ctx context.Context, rows []TrafficRow) error {
	if len(rows) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO traffic_daily (day, scope, rx, tx) VALUES (?, ?, ?, ?)
			ON CONFLICT (day, scope) DO UPDATE SET rx = rx + excluded.rx, tx = tx + excluded.tx`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, r := range rows {
			if _, err := stmt.ExecContext(ctx, r.Day, r.Scope, clamp(r.Rx), clamp(r.Tx)); err != nil {
				return err
			}
		}
		return nil
	})
}

// clamp keeps a byte count within what an INTEGER column holds.
func clamp(n uint64) int64 { return int64(min(n, math.MaxInt64)) }

// TrafficDay returns every scope's totals on day.
func (s *Store) TrafficDay(ctx context.Context, day string) ([]TrafficRow, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT scope, rx, tx FROM traffic_daily WHERE day = ?", day)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrafficRow
	for rows.Next() {
		r := TrafficRow{Day: day}
		var rx, tx int64
		if err := rows.Scan(&r.Scope, &rx, &tx); err != nil {
			return nil, err
		}
		r.Rx, r.Tx = uint64(max(rx, 0)), uint64(max(tx, 0))
		out = append(out, r)
	}
	return out, rows.Err()
}

// TrafficSum returns scope's bytes, down and up together, over the days from from up to but not including to.
func (s *Store) TrafficSum(ctx context.Context, scope, from, to string) (uint64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		"SELECT coalesce(sum(rx + tx), 0) FROM traffic_daily WHERE scope = ? AND day >= ? AND day < ?",
		scope, from, to).Scan(&n)
	return uint64(max(n, 0)), err
}

// TrafficDays returns scope's daily totals on the days from from up to but not including to, by day.
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

// PruneTraffic removes the totals of days more than TrafficKeepDays before now's day (in now's time zone).
func (s *Store) PruneTraffic(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM traffic_daily WHERE day < ?",
		now.AddDate(0, 0, -TrafficKeepDays).Format(time.DateOnly))
	return err
}
