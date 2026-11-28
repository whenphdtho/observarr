package alert

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// What became of an alert on one channel.
const (
	StatusQueued     = "queued"     // waiting to be sent
	StatusSent       = "sent"       // the channel took it
	StatusFailed     = "failed"     // every attempt failed
	StatusHeld       = "held"       // quiet hours: it goes out in a summary afterwards
	StatusSuppressed = "suppressed" // over the channel's hourly limit: counted in a summary
	StatusDropped    = "dropped"    // the channel's queue was full
)

const (
	attempts  = 3  // tries to deliver one message
	queueSize = 32 // messages a channel may have waiting
	maxHeld   = 20 // alerts a channel keeps in quiet hours to list in the summary; beyond, they are counted
)

// gate applies a channel's hourly limit and the quiet hours. It holds no lock: its worker's does.
type gate struct {
	sent       []time.Time // when the messages of the last hour went out, oldest first
	suppressed int
	held       []Message
	heldMore   int
}

// admit decides what becomes of m now: StatusQueued (send it), StatusHeld or StatusSuppressed. In quiet hours only
// a critical alert goes out; that something is over, critical or not, waits for the summary.
func (g *gate) admit(now time.Time, r Rules, m Message) string {
	if (m.Severity != Critical || m.Event == EventResolved) && r.quietAt(now) {
		if len(g.held) < maxHeld {
			g.held = append(g.held, m)
		} else {
			g.heldMore++
		}
		return StatusHeld
	}
	if !g.take(now, r) {
		g.suppressed++
		return StatusSuppressed
	}
	return StatusQueued
}

// take uses one of the hour's messages, if one is left.
func (g *gate) take(now time.Time, r Rules) bool {
	cut := now.Add(-time.Hour)
	i := 0
	for i < len(g.sent) && !g.sent[i].After(cut) {
		i++
	}
	g.sent = g.sent[i:]
	if len(g.sent) >= r.PerHour {
		return false
	}
	g.sent = append(g.sent, now)
	return true
}

// due returns the summaries to send now: the alerts held, once the quiet hours are over, and how many the hourly
// limit suppressed, once there is room again. Each takes one of the hour's messages.
func (g *gate) due(now time.Time, r Rules) []Message {
	var out []Message
	if n := len(g.held) + g.heldMore; n > 0 && !r.quietAt(now) && g.take(now, r) {
		var b strings.Builder
		sev := Info
		for _, m := range g.held {
			fmt.Fprintf(&b, "• %s\n", m.heading())
			if rank(m.Severity) < rank(sev) {
				sev = m.Severity
			}
		}
		if g.heldMore > 0 {
			fmt.Fprintf(&b, "and %d more\n", g.heldMore)
		}
		out = append(out, Message{Event: EventSummary, Severity: sev, Time: now,
			Title: plural(n, "alert", "alerts") + " during quiet hours", Text: strings.TrimSuffix(b.String(), "\n")})
		g.held, g.heldMore = nil, 0
	}
	if g.suppressed > 0 && g.take(now, r) {
		out = append(out, Message{Event: EventSummary, Severity: Warning, Time: now,
			Title: plural(g.suppressed, "more alert", "more alerts") + " suppressed",
			Text:  fmt.Sprintf("At most %d alerts an hour go to this channel; Observarr lists them all.", r.PerHour)})
		g.suppressed = 0
	}
	return out
}

// worker delivers one channel's messages, one at a time, so a slow channel holds up nothing but itself.
type worker struct {
	id     int64
	jobs   chan job
	ctx    context.Context // ends when the channel is removed or disabled, or the Service ends
	cancel context.CancelFunc

	// Guarded by the Service's lock.
	name    string
	rules   []Rule
	sender  Sender
	secrets []string // blanked out of status lines
	gate    gate
}

type job struct {
	logID int64
	m     Message
}

// backoff is the wait before attempt n+1: base doubled per attempt, with ±50 % jitter so channels that failed
// together do not retry together.
func backoff(base time.Duration, n int) time.Duration {
	d := base << (n - 1)
	return d/2 + rand.N(d)
}
