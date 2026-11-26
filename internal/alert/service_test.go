package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"observarr/internal/secret"
	"observarr/internal/shape"
	"observarr/internal/state"
	"observarr/internal/store"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// env is a Service over a real store, with a clock the test moves.
type env struct {
	t     *testing.T
	st    *store.Store
	box   *secret.Box
	hub   *state.Hub
	logs  *syncBuffer
	mu    sync.Mutex
	now   time.Time
	alert *Service
	stop  context.CancelFunc
}

func newEnv(t *testing.T) *env {
	dir := t.TempDir()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	st, err := store.Open(context.Background(), filepath.Join(dir, "o.db"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.LoadOrCreate(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, box: box, hub: state.NewHub(), logs: logs, now: t0}
	e.hub.Put(state.ServiceState{ID: 1, Name: "Sintel"})
	e.hub.Put(state.ServiceState{ID: 2, Name: "Caminandes"})
	return e
}

func (e *env) clock() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

// start starts a Service over the env's store, as a fresh start of Observarr would.
func (e *env) start(r Rules) {
	e.t.Helper()
	body, _ := json.Marshal(r)
	if err := e.st.SetAlertRules(context.Background(), body); err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.alert = New(Config{Store: e.st, Box: e.box, Hub: e.hub, Deps: Deps{HTTP: NewHTTPClient()},
		Log: slog.New(slog.NewTextHandler(e.logs, nil)), Now: e.clock, Every: time.Hour, Backoff: time.Millisecond})
	if err := e.alert.Start(ctx); err != nil {
		e.t.Fatal(err)
	}
	e.stop = func() { cancel(); e.alert.Wait() }
	e.t.Cleanup(e.stop)
}

func (e *env) channel(url string) int64 {
	e.t.Helper()
	sec := Secrets{"url": url}
	ch, err := e.st.CreateAlertChannel(context.Background(), store.AlertChannel{Type: "webhook", Name: "Hook",
		Settings: []byte("{}"), Rules: []string{"down", "health"}, Enabled: true, Tested: true},
		func(id int64) ([]byte, error) { return SealSecrets(e.box, id, sec) })
	if err != nil {
		e.t.Fatal(err)
	}
	return ch.ID
}

func (e *env) down(id int64) {
	e.hub.Record(id, shape.KindHealth, shape.Reading{Error: "connection refused"})
}

func (e *env) up(id int64) {
	e.hub.Record(id, shape.KindHealth, shape.Reading{Data: shape.Health{Warnings: []shape.Warning{}}})
}

func (e *env) log() []store.AlertLogEntry {
	e.t.Helper()
	entries, err := e.st.AlertLog(context.Background(), 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return entries
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
	}
}

func quick() Rules {
	r := DefaultRules()
	r.DownMinutes = 0
	return r
}

func TestDeliveryRetriesThenRecords(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	r.status.Store(http.StatusServiceUnavailable)
	id := e.channel(r.URL + "/hook/secret-path-token")
	e.start(quick())
	e.down(1)
	e.alert.evaluate(context.Background())
	eventually(t, "three attempts", func() bool { return r.count() == 3 })
	eventually(t, "the failure is recorded", func() bool {
		l := e.log()
		return len(l) == 1 && len(l[0].Deliveries) == 1 && l[0].Deliveries[0].Status == StatusFailed
	})
	d := e.log()[0].Deliveries[0]
	if d.Attempts != 3 || d.Note != "HTTP 503 Service Unavailable: {\"error\":\"echo …\"}" {
		t.Fatalf("delivery = %+v", d)
	}
	ch, _ := e.st.AlertChannelByID(context.Background(), id)
	if ch.LastOK || ch.LastNote != d.Note {
		t.Fatalf("channel result = %v %q", ch.LastOK, ch.LastNote)
	}
	if strings.Contains(e.logs.String(), "secret-path-token") {
		t.Fatal("the webhook's secret address reached the log")
	}

	// A refusal is not tried again.
	r.status.Store(http.StatusBadRequest)
	e.down(2)
	e.alert.evaluate(context.Background())
	eventually(t, "the refusal is recorded", func() bool {
		l := e.log()
		return len(l) == 2 && l[0].Deliveries[0].Status == StatusFailed
	})
	if r.count() != 4 || e.log()[0].Deliveries[0].Attempts != 1 {
		t.Fatalf("attempts = %d after a 400", r.count()-3)
	}

	// Recovered: the next one gets through and says so.
	r.status.Store(http.StatusOK)
	e.up(1)
	e.alert.evaluate(context.Background())
	e.advance(time.Minute)
	e.alert.evaluate(context.Background())
	eventually(t, "the recovery is sent", func() bool {
		l := e.log()
		return len(l) == 3 && l[0].Event == EventResolved && l[0].Deliveries[0].Status == StatusSent
	})
	if ch, _ := e.st.AlertChannelByID(context.Background(), id); !ch.LastOK || ch.LastNote != "delivered" {
		t.Fatalf("channel result = %v %q", ch.LastOK, ch.LastNote)
	}
}

func TestRestartDoesNotSendAgain(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	e.channel(r.URL)
	e.start(quick())
	e.down(1)
	e.down(2)
	e.alert.evaluate(context.Background())
	eventually(t, "two alerts sent", func() bool { return r.count() == 2 })
	e.stop()

	e.advance(10 * time.Minute)
	e.start(quick()) // the hub still says both are down, as the first checks after a restart would
	e.alert.evaluate(context.Background())
	e.advance(5 * time.Second)
	e.alert.evaluate(context.Background())
	time.Sleep(50 * time.Millisecond)
	if r.count() != 2 {
		t.Fatalf("%d messages after the restart, want none new", r.count()-2)
	}
	if f := e.alert.Attention(); len(f) != 2 || !f[0].Firing || !f[1].Firing || !f[0].Since.Equal(t0) {
		t.Fatalf("firing = %+v", f)
	}
}

func TestRateLimitSuppressesThenSummarises(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	e.channel(r.URL)
	rules := quick()
	rules.PerHour = 1
	e.start(rules)
	e.down(1)
	e.down(2)
	e.alert.evaluate(context.Background())
	eventually(t, "one message", func() bool { return r.count() == 1 })
	var statuses []string
	for _, l := range e.log() {
		statuses = append(statuses, l.Deliveries[0].Status)
	}
	if strings.Join(statuses, " ") != "suppressed sent" && strings.Join(statuses, " ") != "sent suppressed" {
		t.Fatalf("statuses = %v", statuses)
	}
	e.advance(time.Hour + time.Second)
	e.alert.evaluate(context.Background())
	eventually(t, "the summary", func() bool { return r.count() == 2 })
	if _, body := r.last(t); body["event"] != EventSummary || body["title"] != "1 more alert suppressed" {
		t.Fatalf("summary = %v", body)
	}
}

func TestTestMarksTheChannelTested(t *testing.T) {
	e := newEnv(t)
	r := newReceiver(t)
	id := e.channel(r.URL)
	e.start(quick())
	ch, _ := e.st.AlertChannelByID(context.Background(), id)
	ch.Tested, ch.Enabled = false, false
	if err := e.st.UpdateAlertChannel(context.Background(), ch, ch); err != nil {
		t.Fatal(err)
	}
	if ok, note := e.alert.Test(context.Background(), ch); !ok || note != "delivered" {
		t.Fatalf("Test = %v %q", ok, note)
	}
	if _, body := r.last(t); body["event"] != EventTest {
		t.Fatalf("test body = %v", body)
	}
	if ch, _ := e.st.AlertChannelByID(context.Background(), id); !ch.Tested {
		t.Fatal("a passed test did not mark the channel tested")
	}
}

// The attention list holds every rule's conditions at once, before any debounce, and says which of them fire.
func TestAttentionListsEveryRule(t *testing.T) {
	e := newEnv(t)
	rules := quick()
	rules.Enabled[RuleHealth] = false
	e.start(rules)
	if e.alert.Attention() != nil {
		t.Fatal("an attention list before the first evaluation")
	}
	watch, stop := e.alert.WatchAttention()
	defer stop()
	e.down(1)
	e.hub.Record(2, shape.KindHealth, shape.Reading{Data: shape.Health{Warnings: []shape.Warning{
		{Level: shape.LevelError, Message: "cache out of space"}, {Level: shape.LevelWarning, Message: "slow disk"}}}})
	e.alert.evaluate(context.Background())
	select {
	case <-watch:
	default:
		t.Fatal("no signal after the list changed")
	}
	got := e.alert.Attention()
	if len(got) != 3 || got[0].Severity != Critical || got[2].Severity != Warning {
		t.Fatalf("attention = %+v", got)
	}
	for _, it := range got {
		if it.Firing != (it.Rule == RuleDown) || it.Service == 0 || it.View == "" {
			t.Errorf("item = %+v", it)
		}
	}
	e.alert.evaluate(context.Background())
	select {
	case <-watch:
		t.Fatal("a signal although nothing changed")
	default:
	}
}

// Messages still queued for a channel that is switched off are recorded as dropped, not left queued.
func TestQueuedMessagesAreDroppedWhenTheChannelGoes(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	id := e.channel(srv.URL)
	e.start(quick())
	e.down(1)
	e.down(2)
	e.alert.evaluate(context.Background())
	ch, _ := e.st.AlertChannelByID(context.Background(), id)
	off := ch
	off.Enabled = false
	if err := e.st.UpdateAlertChannel(context.Background(), off, ch); err != nil {
		t.Fatal(err)
	}
	if err := e.alert.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the queued message is dropped", func() bool {
		for _, l := range e.log() {
			if l.Deliveries[0].Status == StatusDropped {
				return true
			}
		}
		return false
	})
	for _, l := range e.log() {
		if d := l.Deliveries[0]; d.Status == StatusQueued {
			t.Fatalf("still queued: %+v", l)
		}
	}
}

// An ntfy channel saved with its topic among the settings has it among the secrets after a start.
func TestTopicMovesIntoTheSecrets(t *testing.T) {
	e := newEnv(t)
	ch, err := e.st.CreateAlertChannel(context.Background(), store.AlertChannel{Type: "ntfy", Name: "Phone",
		Settings: []byte(`{"server":"https://ntfy.sh","topic":"old-topic"}`), Rules: []string{"down"}, Enabled: true,
		Tested: true}, func(id int64) ([]byte, error) { return SealSecrets(e.box, id, Secrets{"token": "tk_12345678"}) })
	if err != nil {
		t.Fatal(err)
	}
	e.start(quick())
	got, _ := e.st.AlertChannelByID(context.Background(), ch.ID)
	sec, err := OpenSecrets(e.box, ch.ID, got.Secrets)
	if err != nil || strings.Contains(string(got.Settings), "old-topic") || sec["topic"] != "old-topic" ||
		sec["token"] != "tk_12345678" || !got.Enabled {
		t.Fatalf("channel = %s %v (%v)", got.Settings, sec, err)
	}
}
