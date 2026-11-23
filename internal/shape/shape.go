// Package shape defines the app-independent data shapes adapters report and the UI renders.
package shape

import (
	"time"
	"unicode/utf8"
)

// Kind names a shape. Adapters declare the kinds they can fill.
type Kind string

const (
	KindHealth Kind = "health"
	KindQueue  Kind = "queue"
)

// Level ranks a warning.
type Level string

const (
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

// Warning is a problem the app itself reports (e.g. an *arr health check).
type Warning struct {
	Level   Level  `json:"level"`
	Message string `json:"message"`
	Source  string `json:"source,omitempty"` // the app's check name, if any
}

// Health is filled by every adapter.
type Health struct {
	Version   string    `json:"version"`
	LatencyMS int64     `json:"latencyMs"`
	Warnings  []Warning `json:"warnings"` // adapters must set []Warning{}, not leave this nil, so it marshals as [] not null
}

// QueueItem is one download/import in progress.
type QueueItem struct {
	Title     string  `json:"title"`
	Status    string  `json:"status"`   // app's status, lower case (downloading, queued, completed, …)
	Progress  float64 `json:"progress"` // 0..1
	SizeBytes int64   `json:"sizeBytes"`
	LeftBytes int64   `json:"leftBytes"`
	ETA       string  `json:"eta,omitempty"`     // app-provided remaining time, as text
	Problem   string  `json:"problem,omitempty"` // why it is stuck/failed, if the app says so
}

// Queue is filled by apps that download or import.
type Queue struct {
	Total int         `json:"total"` // items in the app's queue (may exceed len(Items))
	Items []QueueItem `json:"items"` // at most MaxQueueItems; adapters must set []QueueItem{}, not nil, so it marshals as [] not null
}

// MaxQueueItems bounds the items kept per queue, MaxWarnings the warnings kept per health reading.
const (
	MaxQueueItems = 50
	MaxWarnings   = 50
)

// Reading is one probe result: the data (Health or Queue) or the error text, with its time.
type Reading struct {
	Data  any       `json:"data,omitempty"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

// MaxText bounds free text an app provides and an adapter passes through unmodified: a
// QueueItem's Title, Status, ETA or Problem; a Warning's Message or Source; a Health's Version.
// Every adapter applies Clip to such text before returning it, so one hostile or buggy app can
// never inflate a reading without limit.
const MaxText = 512

// Clip truncates s to at most MaxText bytes, backing off to the nearest earlier UTF-8 rune
// boundary so it never splits a multi-byte rune in half.
func Clip(s string) string { return ClipTo(s, MaxText) }

// ClipTo is Clip with a limit of n bytes.
func ClipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	b := s[:n]
	for len(b) > 0 {
		r, size := utf8.DecodeLastRuneInString(b)
		if r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}
