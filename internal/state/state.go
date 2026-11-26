// Package state holds Observarr's in-memory, live view of every configured service: the latest
// metadata and shape readings the poller has recorded, and a change feed subscribers (the SSE
// handler) can watch.
package state

import (
	"bytes"
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"sync"

	"observarr/internal/shape"
)

// ServiceState is everything the UI shows for one service.
type ServiceState struct {
	ID       int64                        `json:"id"`
	Type     string                       `json:"type"`
	Name     string                       `json:"name"`
	URL      string                       `json:"url"`
	HasKey   bool                         `json:"hasKey"` // a key is saved; never the key itself
	Kinds    []shape.Kind                 `json:"kinds"`
	Up       bool                         `json:"up"` // last health probe succeeded
	Readings map[shape.Kind]shape.Reading `json:"readings"`
	// Container is the linked Docker container, nil when none is linked.
	Container *ContainerBrief `json:"container,omitempty"`
}

// ContainerBrief is what a service card shows of its linked container. Only Name is set while the container is
// unknown: Docker is not configured or no container has that name.
type ContainerBrief struct {
	Name       string  `json:"name"`
	State      string  `json:"state"`  // Docker's: running, exited, restarting, …; "" = unknown
	Health     string  `json:"health"` // "" when the container has no healthcheck
	CPUPercent float64 `json:"cpuPercent"`
	MemBytes   uint64  `json:"memBytes"`
	Restarts   int     `json:"restarts"`
}

// Event is one change sent to subscribers. The same Event value — including the ServiceState
// Service points to — is delivered to every subscriber without copying it per recipient, so a
// received Event (and its Service, Kinds and Readings) must be treated as read-only: do not
// modify one after receiving it, as that would be visible to every other subscriber too.
type Event struct {
	Type    string        `json:"type"` // "service" (added/changed) or "removed"
	Service *ServiceState `json:"service,omitempty"`
	ID      int64         `json:"id,omitempty"` // for "removed"
}

// subscriberBuffer is how many undelivered events a subscriber may queue before it is
// considered slow and dropped. A slow browser must never block probes.
const subscriberBuffer = 64

// Hub holds the current state of every service and fans out changes to subscribers. All
// methods are safe for concurrent use.
//
// Reading.Data is shared by reference between the caller of Record, the Hub's own storage and
// every Event handed to subscribers: once recorded it is never mutated again (by the Hub or by
// callers), so no deep copy of Data itself is needed — only the containing maps and slices
// (Kinds, Readings) are copied, since those are mutable and must not let one caller's edits
// leak into another's view.
type Hub struct {
	mu   sync.Mutex
	svcs map[int64]*entry
	subs map[int]*subscriber
	next int // next subscriber id
	// changed holds a signal after any event, for the one consumer of Changed. It is not a subscriber: it neither
	// counts in Watchers nor can it be dropped.
	changed chan struct{}
}

// entry is a service's state plus the bookkeeping Record needs for change detection.
type entry struct {
	state ServiceState
	cmp   map[shape.Kind][]byte // last comparison bytes per kind, for change detection
}

type subscriber struct {
	ch chan Event
}

// NewHub returns an empty Hub.
func NewHub() *Hub {
	return &Hub{
		svcs:    make(map[int64]*entry),
		subs:    make(map[int]*subscriber),
		changed: make(chan struct{}, 1),
	}
}

// Changed returns a channel that holds a signal when anything changed since it was last received. Changes in a row
// are coalesced into one signal; the receiver reads the state with Snapshot. It is meant for one receiver.
func (h *Hub) Changed() <-chan struct{} { return h.changed }

// Put adds a service or replaces its metadata (Type, Name, URL, HasKey, Kinds, Container). Existing readings and
// Up are kept. It always emits a "service" event, since metadata changes (e.g. a rename) are
// always worth telling subscribers about.
func (h *Hub) Put(s ServiceState) {
	h.mu.Lock()
	defer h.mu.Unlock()

	e, ok := h.svcs[s.ID]
	if !ok {
		e = &entry{
			state: ServiceState{Readings: make(map[shape.Kind]shape.Reading)},
			cmp:   make(map[shape.Kind][]byte),
		}
		h.svcs[s.ID] = e
	}
	e.state.ID = s.ID
	e.state.Type = s.Type
	e.state.Name = s.Name
	e.state.URL = s.URL
	e.state.HasKey = s.HasKey
	e.state.Kinds = slices.Clone(s.Kinds)
	e.state.Container = cloneBrief(s.Container)

	h.emitLocked(Event{Type: "service", Service: cloneState(e.state)})
}

// Remove deletes a service's state. It emits a "removed" event; a removal of an id the Hub
// does not hold is a no-op.
func (h *Hub) Remove(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.svcs[id]; !ok {
		return
	}
	delete(h.svcs, id)
	h.emitLocked(Event{Type: "removed", ID: id})
}

// readingCmp is the part of a Reading that counts for change detection: everything except At.
type readingCmp struct {
	Data  any    `json:"data,omitempty"`
	Error string `json:"error,omitempty"`
}

// Record stores r as the latest reading of kind for service id, and — for shape.KindHealth —
// sets Up from whether r.Error is empty. It always updates the stored reading (so its At stays
// current), but only emits a "service" event when the JSON of r's Data or Error differs from
// what was stored before (a reading that only refreshed At is not a change). Recording against
// an id the Hub does not hold (Put was never called, or it was Removed) is a no-op: there is no
// metadata to show it with.
func (h *Hub) Record(id int64, kind shape.Kind, r shape.Reading) {
	h.mu.Lock()
	defer h.mu.Unlock()

	e, ok := h.svcs[id]
	if !ok {
		return
	}

	key, err := json.Marshal(readingCmp{Data: r.Data, Error: r.Error})
	if err != nil {
		// Data must always be a shape type (or nil); this should not happen. Treat it as an
		// always-visible change rather than silently dropping the reading.
		key = nil
	}
	prev, hadPrev := e.cmp[kind]
	changed := !hadPrev || err != nil || !bytes.Equal(prev, key)

	e.state.Readings[kind] = r
	e.cmp[kind] = key
	if kind == shape.KindHealth {
		e.state.Up = r.Error == ""
	}

	if changed {
		h.emitLocked(Event{Type: "service", Service: cloneState(e.state)})
	}
}

// Snapshot returns every service's current state, deep copied and ordered by Name then ID.
func (h *Hub) Snapshot() []ServiceState {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]ServiceState, 0, len(h.svcs))
	for _, e := range h.svcs {
		out = append(out, *cloneState(e.state))
	}
	slices.SortFunc(out, func(a, b ServiceState) int {
		if c := cmp.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// Subscribe registers a new subscriber and returns its event channel and a cancel function.
// The channel is buffered; a subscriber that falls behind (buffer full) is dropped and its
// channel closed so a slow reader never blocks the Hub. cancel is idempotent and safe to call
// more than once or concurrently with delivery. Events are shared, unmodified, with every
// subscriber (see the Event doc comment); a received Event must not be mutated.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	h.mu.Lock()
	id := h.next
	h.next++
	ch := make(chan Event, subscriberBuffer)
	h.subs[id] = &subscriber{ch: ch}
	h.mu.Unlock()

	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if sub, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(sub.ch)
		}
	}
	return ch, cancel
}

// Watchers returns the current number of live subscribers.
func (h *Hub) Watchers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// emitLocked sends ev to every subscriber. Callers must hold h.mu. A subscriber whose buffer
// is full is dropped and its channel closed, under the same lock used by cancel and Subscribe,
// so no send ever races a close.
func (h *Hub) emitLocked(ev Event) {
	select {
	case h.changed <- struct{}{}:
	default:
	}
	for id, sub := range h.subs {
		select {
		case sub.ch <- ev:
		default:
			delete(h.subs, id)
			close(sub.ch)
		}
	}
}

// cloneState returns a deep copy of s's Kinds slice and Readings map (Reading.Data is shared;
// see the Hub doc comment).
func cloneState(s ServiceState) *ServiceState {
	clone := s
	clone.Kinds = slices.Clone(s.Kinds)
	clone.Readings = maps.Clone(s.Readings)
	clone.Container = cloneBrief(s.Container)
	if clone.Readings == nil {
		clone.Readings = make(map[shape.Kind]shape.Reading)
	}
	return &clone
}

func cloneBrief(b *ContainerBrief) *ContainerBrief {
	if b == nil {
		return nil
	}
	c := *b
	return &c
}
