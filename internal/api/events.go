package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"observarr/internal/state"
	"observarr/internal/store"
	"observarr/internal/system"
)

const (
	// maxStreams bounds open live event streams server-wide; each holds a connection and a hub subscription.
	maxStreams = 32
	// maxSessionStreams bounds one session's open streams, so a single session cannot take every slot.
	maxSessionStreams = 4
	// streamWriteTimeout ends a stream whose client stops reading, instead of blocking on it forever.
	streamWriteTimeout = 10 * time.Second
)

// events streams live state as Server-Sent Events: first a "snapshot" of every service and a "system" event with the
// host and container figures, then a "service" or "removed" event per change, a "system" event per new sample (at
// most one per systemEvery; a newer sample replaces one not sent yet), and a ping comment now and then so proxies
// keep the connection open. The stream ends when the client leaves, when the server shuts down (the request context
// ends), when its session is ended by another one's "sign out others", when the session is found gone at a ping
// (logged out or expired), or when the hub drops the subscription because the client fell behind; the browser then
// reconnects and gets a fresh snapshot.
func (s *Server) events(w http.ResponseWriter, r *http.Request, sess store.Session) {
	if r.Method != http.MethodGet { // the "GET" route also matches HEAD, which would hold a slot for nothing
		writeError(w, http.StatusNotFound, "unknown API endpoint")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	slot, ok := s.openStream(sess.CSRF, cancel)
	if !ok {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "too many live connections; try again shortly")
		return
	}
	defer s.closeStream(sess.CSRF, slot)

	// Subscribe before taking the snapshot: a change in between then arrives twice, which is harmless since
	// every event carries the whole service, instead of not at all.
	events, cancel := s.Hub.Subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx would otherwise buffer the stream
	h.Add("Vary", "Accept-Encoding")
	rc := http.NewResponseController(w)
	// Consecutive "system" events are mostly the same text, which gzip finds in its window: the stream shrinks
	// far more than one response would. Its events hold no secret (see writeBulkJSON). The fastest level finds
	// those repeats too and holds a quarter less memory for as long as the stream is open.
	var out io.Writer = w
	var zw *gzip.Writer
	if accepts(r, "gzip") {
		h.Set("Content-Encoding", "gzip")
		zw, _ = gzip.NewWriterLevel(w, gzip.BestSpeed) // fails only for an invalid level
		// Close ends the gzip stream; on a broken connection it fails quietly.
		defer zw.Close()
		out = zw
	}
	send := func(msg []byte) bool {
		if err := rc.SetWriteDeadline(time.Now().Add(streamWriteTimeout)); err != nil {
			return false
		}
		if _, err := out.Write(msg); err != nil {
			return false
		}
		if zw != nil && zw.Flush() != nil { // every event goes out whole, not once gzip's buffer fills
			return false
		}
		return rc.Flush() == nil
	}

	var (
		samples    <-chan system.Snapshot // nil without a sampler: never ready
		pending    *system.Snapshot       // the latest sample, held back until flush
		flush      <-chan time.Time
		lastSample time.Time
	)
	if s.System != nil {
		var stop func()
		samples, stop = s.System.Subscribe()
		defer stop()
	}
	var buf bytes.Buffer // holds one encoded event at a time, reused so that events cost no new buffer
	if !send(s.event(&buf, "snapshot", s.Hub.Snapshot())) || !send(s.event(&buf, "system", s.systemSnapshot())) {
		return
	}
	lastSample = time.Now()
	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	for {
		var msg []byte
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			msg = s.change(&buf, ev)
		case snap := <-samples:
			if wait := s.systemEvery - time.Since(lastSample); wait > 0 {
				if pending == nil {
					flush = time.After(wait)
				}
				pending = &snap
				continue
			}
			msg, lastSample = s.event(&buf, "system", snap), time.Now()
			pending, flush = nil, nil // older than snap: it must not follow it
		case <-flush:
			msg, lastSample = s.event(&buf, "system", *pending), time.Now()
			pending, flush = nil, nil
		case <-ping.C:
			if _, err := s.Sessions.Lookup(r); err != nil {
				return
			}
			msg = []byte(": ping\n\n")
		}
		if !send(msg) {
			return
		}
	}
}

// streamSlot is one open live event stream; end stops it.
type streamSlot struct{ end context.CancelFunc }

// openStream takes a stream slot for the session whose CSRF token is key (unique per session), or reports false
// when the server or the session has none left. end stops the stream.
func (s *Server) openStream(key string, end context.CancelFunc) (*streamSlot, bool) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if len(s.sessionStreams[key]) >= maxSessionStreams || s.streams.Load() >= maxStreams {
		return nil, false
	}
	if s.sessionStreams == nil {
		s.sessionStreams = map[string]map[*streamSlot]bool{}
	}
	if s.sessionStreams[key] == nil {
		s.sessionStreams[key] = map[*streamSlot]bool{}
	}
	slot := &streamSlot{end: end}
	s.sessionStreams[key][slot] = true
	s.streams.Add(1)
	return slot, true
}

// closeStream gives back a slot openStream took.
func (s *Server) closeStream(key string, slot *streamSlot) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	if delete(s.sessionStreams[key], slot); len(s.sessionStreams[key]) == 0 {
		delete(s.sessionStreams, key)
	}
	s.streams.Add(-1)
}

// endOtherStreams stops the streams of every session but the one whose CSRF token is keep.
func (s *Server) endOtherStreams(keep string) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	for key, slots := range s.sessionStreams {
		if key != keep {
			for slot := range slots {
				slot.end()
			}
		}
	}
}

// change encodes a hub event into buf.
func (s *Server) change(buf *bytes.Buffer, ev state.Event) []byte {
	if ev.Type == "removed" {
		return s.event(buf, "removed", struct {
			ID int64 `json:"id"`
		}{ev.ID})
	}
	return s.event(buf, "service", ev.Service)
}

// event encodes one Server-Sent Event into buf, replacing what it held, and returns it; the result is valid until
// buf is used again. JSON never contains a raw newline, so the data fits on one line.
func (s *Server) event(buf *bytes.Buffer, name string, data any) []byte {
	buf.Reset()
	buf.WriteString("event: " + name + "\ndata: ")
	if err := json.NewEncoder(buf).Encode(data); err != nil { // only shapes are sent, and they always encode
		s.Log.Error("encoding a live event failed", "event", name, "err", err)
		buf.Reset()
		buf.WriteString(": encoding failed\n")
	}
	buf.WriteByte('\n') // Encode ended the data line; an empty line ends the event
	return buf.Bytes()
}
