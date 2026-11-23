package state

import (
	"fmt"
	"testing"
	"time"

	"observarr/internal/shape"
)

func baseService(id int64, name string) ServiceState {
	return ServiceState{
		ID:    id,
		Type:  "sonarr",
		Name:  name,
		URL:   "http://localhost:8989",
		Kinds: []shape.Kind{shape.KindHealth, shape.KindQueue},
	}
}

// drain reads one event with a generous timeout, failing the test if none arrives.
func drain(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

// expectNone asserts no event arrives within a short window.
func expectNone(t *testing.T, ch <-chan Event) {
	t.Helper()
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestPutSnapshotCopySemantics(t *testing.T) {
	h := NewHub()
	h.Put(baseService(1, "Sonarr"))

	snap := h.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("Snapshot: got %d entries, want 1", len(snap))
	}
	// Mutate the returned slice/map; the hub must be unaffected.
	snap[0].Kinds[0] = shape.Kind("mutated")
	snap[0].Name = "mutated"
	snap[0].Readings[shape.KindHealth] = shape.Reading{Error: "mutated"}

	snap2 := h.Snapshot()
	if snap2[0].Kinds[0] != shape.KindHealth {
		t.Errorf("Kinds mutation leaked into hub: got %v", snap2[0].Kinds)
	}
	if snap2[0].Name != "Sonarr" {
		t.Errorf("Name mutation leaked into hub: got %q", snap2[0].Name)
	}
	if _, ok := snap2[0].Readings[shape.KindHealth]; ok {
		t.Errorf("Readings mutation leaked into hub: got %+v", snap2[0].Readings)
	}
}

func TestPutEmitsServiceEvent(t *testing.T) {
	h := NewHub()
	events, cancel := h.Subscribe()
	defer cancel()

	h.Put(baseService(1, "Sonarr"))

	ev := drain(t, events)
	if ev.Type != "service" {
		t.Errorf("Type: got %q, want %q", ev.Type, "service")
	}
	if ev.Service == nil || ev.Service.ID != 1 || ev.Service.Name != "Sonarr" {
		t.Errorf("Service: got %+v", ev.Service)
	}
}

func TestPutKeepsReadings(t *testing.T) {
	h := NewHub()
	h.Put(baseService(1, "Sonarr"))
	h.Record(1, shape.KindHealth, shape.Reading{Data: shape.Health{Version: "1.0"}, At: time.Now()})

	// Replace metadata (e.g. rename); readings must survive.
	renamed := baseService(1, "Sonarr Renamed")
	h.Put(renamed)

	snap := h.Snapshot()
	if snap[0].Name != "Sonarr Renamed" {
		t.Fatalf("Name: got %q", snap[0].Name)
	}
	r, ok := snap[0].Readings[shape.KindHealth]
	if !ok {
		t.Fatal("reading lost after Put")
	}
	health, ok := r.Data.(shape.Health)
	if !ok || health.Version != "1.0" {
		t.Errorf("reading data: got %+v", r.Data)
	}
}

func TestRecordEmitsOnceForEqualReadings(t *testing.T) {
	h := NewHub()
	h.Put(baseService(1, "Sonarr"))
	events, cancel := h.Subscribe()
	defer cancel()

	reading := shape.Reading{Data: shape.Health{Version: "1.0"}, At: time.Now()}
	h.Record(1, shape.KindHealth, reading)
	ev := drain(t, events)
	if ev.Type != "service" {
		t.Fatalf("first Record: got event type %q", ev.Type)
	}

	// Equal Data, different At: must not emit again.
	reading2 := shape.Reading{Data: shape.Health{Version: "1.0"}, At: time.Now().Add(time.Second)}
	h.Record(1, shape.KindHealth, reading2)
	expectNone(t, events)

	// But the stored reading's At should still have advanced.
	snap := h.Snapshot()
	got := snap[0].Readings[shape.KindHealth]
	if !got.At.Equal(reading2.At) {
		t.Errorf("At: got %v, want %v", got.At, reading2.At)
	}
}

func TestRecordEmitsOnChangedData(t *testing.T) {
	h := NewHub()
	h.Put(baseService(1, "Sonarr"))
	events, cancel := h.Subscribe()
	defer cancel()

	h.Record(1, shape.KindHealth, shape.Reading{Data: shape.Health{Version: "1.0"}, At: time.Now()})
	drain(t, events)

	h.Record(1, shape.KindHealth, shape.Reading{Data: shape.Health{Version: "2.0"}, At: time.Now()})
	ev := drain(t, events)
	if ev.Type != "service" {
		t.Fatalf("got event type %q", ev.Type)
	}
}

func TestUpFollowsHealthErrors(t *testing.T) {
	h := NewHub()
	h.Put(baseService(1, "Sonarr"))

	h.Record(1, shape.KindHealth, shape.Reading{Data: shape.Health{Version: "1.0"}, At: time.Now()})
	snap := h.Snapshot()
	if !snap[0].Up {
		t.Fatalf("Up: got false after a successful health reading")
	}

	h.Record(1, shape.KindHealth, shape.Reading{Error: "connection refused", At: time.Now()})
	snap = h.Snapshot()
	if snap[0].Up {
		t.Fatalf("Up: got true after an errored health reading")
	}

	h.Record(1, shape.KindHealth, shape.Reading{Data: shape.Health{Version: "1.0"}, At: time.Now()})
	snap = h.Snapshot()
	if !snap[0].Up {
		t.Fatalf("Up: got false after recovery")
	}
}

func TestRecordDoesNotAffectUpForNonHealthKind(t *testing.T) {
	h := NewHub()
	h.Put(baseService(1, "Sonarr"))
	h.Record(1, shape.KindHealth, shape.Reading{Data: shape.Health{Version: "1.0"}, At: time.Now()})
	h.Record(1, shape.KindQueue, shape.Reading{Error: "boom", At: time.Now()})
	snap := h.Snapshot()
	if !snap[0].Up {
		t.Errorf("Up: a queue error must not affect Up (health-derived)")
	}
}

func TestRecordOnUnknownIDIsNoop(t *testing.T) {
	h := NewHub()
	events, cancel := h.Subscribe()
	defer cancel()

	h.Record(999, shape.KindHealth, shape.Reading{Data: shape.Health{Version: "1.0"}, At: time.Now()})
	expectNone(t, events)

	if len(h.Snapshot()) != 0 {
		t.Errorf("Snapshot: got entries after Record on unknown id")
	}
}

func TestRemoveEmitsRemoved(t *testing.T) {
	h := NewHub()
	h.Put(baseService(1, "Sonarr"))
	events, cancel := h.Subscribe()
	defer cancel()

	h.Remove(1)
	ev := drain(t, events)
	if ev.Type != "removed" {
		t.Errorf("Type: got %q, want %q", ev.Type, "removed")
	}
	if ev.ID != 1 {
		t.Errorf("ID: got %d, want 1", ev.ID)
	}
	if ev.Service != nil {
		t.Errorf("Service: got %+v, want nil", ev.Service)
	}

	if len(h.Snapshot()) != 0 {
		t.Errorf("Snapshot: got entries after Remove")
	}
}

func TestRemoveUnknownIDIsNoop(t *testing.T) {
	h := NewHub()
	events, cancel := h.Subscribe()
	defer cancel()
	h.Remove(42)
	expectNone(t, events)
}

func TestSlowSubscriberDroppedAndClosed(t *testing.T) {
	h := NewHub()

	slow, cancelSlow := h.Subscribe()
	defer cancelSlow()
	fast, cancelFast := h.Subscribe()
	defer cancelFast()

	if h.Watchers() != 2 {
		t.Fatalf("Watchers: got %d, want 2", h.Watchers())
	}

	h.Put(baseService(1, "Sonarr"))

	// Drain the Put event from fast only; slow never reads, so its buffer (64) fills and it
	// gets dropped once enough further events are produced.
	drain(t, fast)
	<-slow // consume the Put event so the subsequent loop fills exactly to the brim

	for i := 0; i < 65; i++ {
		h.Record(1, shape.KindHealth, shape.Reading{Data: shape.Health{Version: fmt.Sprintf("v%d", i)}, At: time.Now()})
		drain(t, fast)
	}

	// slow's channel must now be closed (dropped): it may still hold the backlog it never
	// read, but draining it fully must reach a closed, empty channel rather than block.
	closed := false
	for i := 0; i < subscriberBuffer+1 && !closed; i++ {
		select {
		case _, ok := <-slow:
			if !ok {
				closed = true
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for slow subscriber's channel to close")
		}
	}
	if !closed {
		t.Fatal("slow subscriber's channel never closed")
	}

	if h.Watchers() != 1 {
		t.Errorf("Watchers: got %d, want 1 after slow subscriber dropped", h.Watchers())
	}

	// fast must still be receiving fine (checked throughout the loop above).
}

func TestCancelIdempotent(t *testing.T) {
	h := NewHub()
	_, cancel := h.Subscribe()
	cancel()
	cancel() // must not panic
}

func TestWatchersCount(t *testing.T) {
	h := NewHub()
	if h.Watchers() != 0 {
		t.Fatalf("Watchers: got %d, want 0", h.Watchers())
	}
	_, c1 := h.Subscribe()
	_, c2 := h.Subscribe()
	if h.Watchers() != 2 {
		t.Fatalf("Watchers: got %d, want 2", h.Watchers())
	}
	c1()
	if h.Watchers() != 1 {
		t.Fatalf("Watchers: got %d, want 1", h.Watchers())
	}
	c2()
	if h.Watchers() != 0 {
		t.Fatalf("Watchers: got %d, want 0", h.Watchers())
	}
}

func TestSnapshotOrderedByNameThenID(t *testing.T) {
	h := NewHub()
	h.Put(baseService(3, "Bravo"))
	h.Put(baseService(1, "Alpha"))
	h.Put(baseService(2, "Alpha"))

	snap := h.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("Snapshot: got %d entries, want 3", len(snap))
	}
	want := []int64{1, 2, 3}
	for i, id := range want {
		if snap[i].ID != id {
			t.Errorf("snap[%d].ID: got %d, want %d (order: %+v)", i, snap[i].ID, id, snap)
		}
	}
}

func TestConcurrentRecordSubscribeCancel(t *testing.T) {
	h := NewHub()
	h.Put(baseService(1, "Sonarr"))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			h.Record(1, shape.KindHealth, shape.Reading{Data: shape.Health{Version: fmt.Sprintf("v%d", i)}, At: time.Now()})
		}
	}()

	for i := 0; i < 50; i++ {
		ch, cancel := h.Subscribe()
		go func(ch <-chan Event, cancel func()) {
			// A goroutine that drains a few events then cancels, racing with Record and the hub's
			// own drop-on-full logic.
			for j := 0; j < 3; j++ {
				select {
				case <-ch:
				case <-time.After(50 * time.Millisecond):
				}
			}
			cancel()
			cancel()
		}(ch, cancel)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Record loop did not finish in time")
	}
}
