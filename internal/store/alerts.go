package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// AlertChannel is one destination for alerts as stored. Settings is JSON and holds nothing secret; Secrets is the
// sealed JSON of the secret fields, nil when the channel has none.
type AlertChannel struct {
	ID       int64
	Type     string
	Name     string
	Settings []byte
	Secrets  []byte
	Rules    []string // the rules whose alerts this channel receives
	Enabled  bool
	Tested   bool // a test got through since Settings or Secrets last changed
	LastAt   time.Time
	LastOK   bool
	LastNote string
}

// maxAlertLog is how many alert log entries PruneAlertLog keeps.
const maxAlertLog = 1000

const channelColumns = "id, type, name, settings, secrets, rules, enabled, tested, last_at, last_ok, last_note"

// CreateAlertChannel inserts ch, then seals its secrets for the new row id with seal, in one transaction as
// CreateService does. It returns ErrExists when the name is taken.
func (s *Store) CreateAlertChannel(ctx context.Context, ch AlertChannel, seal func(id int64) ([]byte, error)) (AlertChannel, error) {
	var created AlertChannel
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		created, err = createAlertChannel(ctx, tx, ch, seal)
		return err
	})
	return created, err
}

func createAlertChannel(ctx context.Context, tx *sql.Tx, ch AlertChannel, seal func(id int64) ([]byte, error)) (AlertChannel, error) {
	rules, err := json.Marshal(ch.Rules)
	if err != nil {
		return AlertChannel{}, err
	}
	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx, `INSERT INTO alert_channels (type, name, settings, rules, enabled, tested,
		created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, ch.Type, ch.Name, ch.Settings, rules, ch.Enabled,
		ch.Tested, now, now)
	if isUniqueViolation(err) {
		return AlertChannel{}, ErrExists
	}
	if err != nil {
		return AlertChannel{}, err
	}
	if ch.ID, err = res.LastInsertId(); err != nil {
		return AlertChannel{}, err
	}
	if ch.Secrets, err = seal(ch.ID); err != nil {
		return AlertChannel{}, fmt.Errorf("seal channel secrets: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE alert_channels SET secrets = ? WHERE id = ?", ch.Secrets, ch.ID); err != nil {
		return AlertChannel{}, err
	}
	return ch, nil
}

// AlertChannels returns every channel, ordered by name. It never returns a nil slice.
func (s *Store) AlertChannels(ctx context.Context) ([]AlertChannel, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+channelColumns+" FROM alert_channels ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertChannel{}
	for rows.Next() {
		ch, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// AlertChannelByID returns the channel with id, or ErrNotFound.
func (s *Store) AlertChannelByID(ctx context.Context, id int64) (AlertChannel, error) {
	ch, err := scanChannel(s.db.QueryRowContext(ctx, "SELECT "+channelColumns+" FROM alert_channels WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return AlertChannel{}, ErrNotFound
	}
	return ch, err
}

func scanChannel(row interface{ Scan(...any) error }) (AlertChannel, error) {
	var ch AlertChannel
	var rules []byte
	var lastAt int64
	if err := row.Scan(&ch.ID, &ch.Type, &ch.Name, &ch.Settings, &ch.Secrets, &rules, &ch.Enabled, &ch.Tested,
		&lastAt, &ch.LastOK, &ch.LastNote); err != nil {
		return AlertChannel{}, err
	}
	if lastAt > 0 {
		ch.LastAt = time.Unix(lastAt, 0)
	}
	if err := json.Unmarshal(rules, &ch.Rules); err != nil {
		return AlertChannel{}, fmt.Errorf("channel %d: rules: %w", ch.ID, err)
	}
	return ch, nil
}

// UpdateAlertChannel saves the name, settings, secrets, rules, enabled and tested of the channel ch.ID, but only
// while it still has the settings and secrets of was: a change made meanwhile is ErrConflict. It returns
// ErrNotFound, or ErrExists when the name is taken.
func (s *Store) UpdateAlertChannel(ctx context.Context, ch, was AlertChannel) error {
	rules, err := json.Marshal(ch.Rules)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE alert_channels SET name = ?, settings = ?, secrets = ?, rules = ?,
		enabled = ?, tested = ?, updated_at = ? WHERE id = ? AND settings = ? AND secrets IS ?`,
		ch.Name, ch.Settings, ch.Secrets, rules, ch.Enabled, ch.Tested, time.Now().Unix(), ch.ID, was.Settings, was.Secrets)
	if isUniqueViolation(err) {
		return ErrExists
	}
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	if _, err := s.AlertChannelByID(ctx, ch.ID); err != nil {
		return err
	}
	return ErrConflict
}

// ErrConflict is returned when a row changed between reading and writing it.
var ErrConflict = errors.New("changed meanwhile")

// SetAlertChannelResult records the latest delivery or test of channel id. passed marks the channel tested, but
// only while it still has the settings and secrets the test used, so a test of an older version never vouches for
// a newer one.
func (s *Store) SetAlertChannelResult(ctx context.Context, id int64, at time.Time, ok bool, note string,
	passed bool, settings, secrets []byte) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_channels SET last_at = ?, last_ok = ?, last_note = ?,
		tested = tested OR (? AND settings = ? AND secrets IS ?) WHERE id = ?`, at.Unix(), ok, note, passed, settings,
		secrets, id)
	return err
}

// DeleteAlertChannel removes the channel id, or returns ErrNotFound.
func (s *Store) DeleteAlertChannel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM alert_channels WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	return ErrNotFound
}

// AlertRules returns the saved rules document, nil when none was saved.
func (s *Store) AlertRules(ctx context.Context) ([]byte, error) {
	var body []byte
	err := s.db.QueryRowContext(ctx, "SELECT body FROM alert_rules WHERE id = 1").Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return body, err
}

// SetAlertRules saves the rules document.
func (s *Store) SetAlertRules(ctx context.Context, body []byte) error {
	_, err := s.db.ExecContext(ctx,
		"INSERT INTO alert_rules (id, body) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET body = excluded.body", body)
	return err
}

// AlertState is one firing alert as kept across restarts. Body is the alert's condition as JSON.
type AlertState struct {
	Key      string
	Body     []byte
	Since    time.Time
	Notified time.Time // zero: never sent
}

// AlertStates returns every saved firing alert.
func (s *Store) AlertStates(ctx context.Context) ([]AlertState, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key, body, since, notified FROM alert_state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertState
	for rows.Next() {
		var a AlertState
		var since, notified int64
		if err := rows.Scan(&a.Key, &a.Body, &since, &notified); err != nil {
			return nil, err
		}
		a.Since = time.Unix(since, 0)
		if notified > 0 {
			a.Notified = time.Unix(notified, 0)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaveAlertStates writes put and removes the keys in del, in one transaction.
func (s *Store) SaveAlertStates(ctx context.Context, put []AlertState, del []string) error {
	if len(put) == 0 && len(del) == 0 {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, a := range put {
			var notified int64
			if !a.Notified.IsZero() {
				notified = a.Notified.Unix()
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO alert_state (key, body, since, notified) VALUES (?, ?, ?, ?)
				ON CONFLICT (key) DO UPDATE SET body = excluded.body, since = excluded.since, notified = excluded.notified`,
				a.Key, a.Body, a.Since.Unix(), notified); err != nil {
				return err
			}
		}
		for _, key := range del {
			if _, err := tx.ExecContext(ctx, "DELETE FROM alert_state WHERE key = ?", key); err != nil {
				return err
			}
		}
		return nil
	})
}

// AlertLogEntry is one alert sent (or meant to be sent), with what became of it on each channel.
type AlertLogEntry struct {
	ID         int64
	At         time.Time
	Event      string
	Rule       string
	Severity   string
	Service    string
	Title      string
	Message    string
	Deliveries []AlertDelivery
}

// AlertDelivery is what became of one alert on one channel.
type AlertDelivery struct {
	ChannelID int64
	Channel   string
	Status    string
	Attempts  int
	Note      string
	At        time.Time
}

// AddAlertLog writes e with its deliveries and returns its id.
func (s *Store) AddAlertLog(ctx context.Context, e AlertLogEntry) (int64, error) {
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO alert_log (at, event, rule, severity, service, title, message)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, e.At.Unix(), e.Event, e.Rule, e.Severity, e.Service, e.Title, e.Message)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		for _, d := range e.Deliveries {
			if err := putDelivery(ctx, tx, id, d); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// SetAlertDelivery records what became of log entry logID on a channel. An entry pruned meanwhile is left gone.
func (s *Store) SetAlertDelivery(ctx context.Context, logID int64, d AlertDelivery) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM alert_log WHERE id = ?)", logID).Scan(&exists); err != nil || !exists {
			return err
		}
		return putDelivery(ctx, tx, logID, d)
	})
}

func putDelivery(ctx context.Context, tx *sql.Tx, logID int64, d AlertDelivery) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO alert_deliveries (log_id, channel_id, channel, status, attempts, note, at)
		VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (log_id, channel_id) DO UPDATE SET status = excluded.status,
		attempts = excluded.attempts, note = excluded.note, at = excluded.at`,
		logID, d.ChannelID, d.Channel, d.Status, d.Attempts, d.Note, d.At.Unix())
	return err
}

// AlertLog returns the newest log entries, at most limit, newest first, each with its deliveries.
func (s *Store) AlertLog(ctx context.Context, limit int) ([]AlertLogEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT l.id, l.at, l.event, l.rule, l.severity, l.service, l.title, l.message,
		d.channel_id, d.channel, d.status, d.attempts, d.note, d.at
		FROM (SELECT * FROM alert_log ORDER BY id DESC LIMIT ?) l
		LEFT JOIN alert_deliveries d ON d.log_id = l.id ORDER BY l.id DESC, d.channel COLLATE NOCASE`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertLogEntry{}
	for rows.Next() {
		var e AlertLogEntry
		var at int64
		var chID, attempts, dAt sql.NullInt64
		var channel, status, note sql.NullString
		if err := rows.Scan(&e.ID, &at, &e.Event, &e.Rule, &e.Severity, &e.Service, &e.Title, &e.Message,
			&chID, &channel, &status, &attempts, &note, &dAt); err != nil {
			return nil, err
		}
		if n := len(out); n == 0 || out[n-1].ID != e.ID {
			e.At = time.Unix(at, 0)
			e.Deliveries = []AlertDelivery{}
			out = append(out, e)
		}
		if chID.Valid {
			last := &out[len(out)-1]
			last.Deliveries = append(last.Deliveries, AlertDelivery{ChannelID: chID.Int64, Channel: channel.String,
				Status: status.String, Attempts: int(attempts.Int64), Note: note.String, At: time.Unix(dAt.Int64, 0)})
		}
	}
	return out, rows.Err()
}

// PruneAlertLog keeps the newest maxAlertLog log entries; their deliveries go with them.
func (s *Store) PruneAlertLog(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		"DELETE FROM alert_log WHERE id <= (SELECT id FROM alert_log ORDER BY id DESC LIMIT 1 OFFSET ?)", maxAlertLog)
	return err
}

// AlertImport is the alert part of a settings import: channels sealed as CreateAlertChannel does (seal gets the
// index in Channels and the new row id), and a rules document (nil keeps the saved one).
type AlertImport struct {
	Channels []AlertChannel
	Seal     func(i int, id int64) ([]byte, error)
	Rules    []byte
}

// importAlerts adds a's channels in tx, after removing the saved ones when replace is set.
func importAlerts(ctx context.Context, tx *sql.Tx, replace bool, a AlertImport) ([]AlertChannel, error) {
	if replace {
		if _, err := tx.ExecContext(ctx, "DELETE FROM alert_channels"); err != nil {
			return nil, err
		}
	}
	created := make([]AlertChannel, 0, len(a.Channels))
	for i, ch := range a.Channels {
		c, err := createAlertChannel(ctx, tx, ch, func(id int64) ([]byte, error) { return a.Seal(i, id) })
		if err != nil {
			return nil, err
		}
		created = append(created, c)
	}
	if a.Rules != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO alert_rules (id, body) VALUES (1, ?)
			ON CONFLICT (id) DO UPDATE SET body = excluded.body`, a.Rules); err != nil {
			return nil, err
		}
	}
	return created, nil
}
