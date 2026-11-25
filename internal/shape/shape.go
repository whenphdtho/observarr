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

// KindSecurity is filled by apps that block addresses (an intrusion prevention system).
const KindSecurity Kind = "security"

// SecurityCount is one row of a breakdown: a name (a decision type, an origin, a scenario) and how many active
// decisions carry it.
type SecurityCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SecurityDecision is one active decision against an address, as the app reports it.
type SecurityDecision struct {
	Value    string    `json:"value"`          // the address, range or other value, as given
	Scope    string    `json:"scope"`          // what Value is: an IP, a range, a country, …
	Type     string    `json:"type"`           // ban, captcha, …
	Origin   string    `json:"origin"`         // where the decision came from
	Scenario string    `json:"scenario"`       // the behaviour that triggered it
	Until    time.Time `json:"until,omitzero"` // when it ends; zero (left out) if the app did not say
}

// Security sums up the decisions an app enforces: counts over all of them, and the most recent ones made on this
// machine (by its own detections or by hand) rather than taken from shared blocklists.
type Security struct {
	Total     int  `json:"total"`     // active decisions counted
	Local     int  `json:"local"`     // of them, made on this machine
	Community int  `json:"community"` // of them, from shared blocklists
	Capped    bool `json:"capped"`    // counting stopped early: the real totals are higher
	// ByType, ByOrigin and ByScope count all decisions, TopScenarios only the local ones. Each is sorted by count,
	// at most MaxSecurityCounts long, and never nil.
	ByType       []SecurityCount    `json:"byType"`
	ByOrigin     []SecurityCount    `json:"byOrigin"`
	ByScope      []SecurityCount    `json:"byScope"`
	TopScenarios []SecurityCount    `json:"topScenarios"`
	Recent       []SecurityDecision `json:"recent"` // local decisions, newest first, at most MaxSecurityDecisions; never nil
}

// MaxSecurityDecisions bounds Security.Recent, MaxSecurityCounts each of its breakdowns.
const (
	MaxSecurityDecisions = 50
	MaxSecurityCounts    = 10
)

// KindMounts is filled by apps that mount remote storage and cache it on a local disk.
const KindMounts Kind = "mounts"

// Mount states.
const (
	MountMounted = "mounted" // the app reports it as mounted at MountPoint
	MountActive  = "active"  // the app serves it but does not report where, or whether, it is mounted
	// MountUnmounted is a mount the app knows of that is not mounted now. No adapter reports it yet; it is for apps
	// that keep configured mounts.
	MountUnmounted = "unmounted"
)

// Mount is one remote file system an app mounts, with its local cache.
type Mount struct {
	Remote     string `json:"remote"`               // what is mounted, e.g. "cloud:media"
	MountPoint string `json:"mountPoint,omitempty"` // where, if the app reports it
	State      string `json:"state"`                // MountMounted, MountActive or MountUnmounted
	// Unlisted is for MountActive: the app lists the mounts made through it, and this one is not among them.
	Unlisted          bool   `json:"unlisted,omitempty"`
	CacheMode         string `json:"cacheMode,omitempty"` // the app's cache mode, e.g. "full"; "" if unknown
	Cache             bool   `json:"cache"`               // the mount keeps a disk cache; the figures below are its own
	CacheUsedBytes    int64  `json:"cacheUsedBytes"`
	CacheMaxBytes     int64  `json:"cacheMaxBytes"` // 0: no limit
	CachedFiles       int64  `json:"cachedFiles"`
	UploadsInProgress int64  `json:"uploadsInProgress"`
	UploadsQueued     int64  `json:"uploadsQueued"`
	ErroredFiles      int64  `json:"erroredFiles"` // cached files the app failed to write back
	OutOfSpace        bool   `json:"outOfSpace"`   // the cache disk is full
}

// Transfer is one transfer in progress.
type Transfer struct {
	Name      string  `json:"name"`
	Progress  float64 `json:"progress"` // 0..1
	SizeBytes int64   `json:"sizeBytes"`
	Speed     float64 `json:"speed"` // bytes/s
}

// Mounts is filled by apps that mount remote storage.
type Mounts struct {
	Total           int        `json:"total"`           // mounts the app reports (may exceed len(Mounts))
	Mounts          []Mount    `json:"mounts"`          // at most MaxMounts; [] not nil
	Speed           float64    `json:"speed"`           // transfer speed now, bytes/s
	Bytes           int64      `json:"bytes"`           // bytes transferred since the app started
	Errors          int64      `json:"errors"`          // transfer errors since the app started
	ActiveTransfers int        `json:"activeTransfers"` // transfers in progress (may exceed len(Transfers))
	Transfers       []Transfer `json:"transfers"`       // at most MaxTransfers; [] not nil
}

// MaxMounts bounds the mounts kept per reading, MaxTransfers the transfers.
const (
	MaxMounts    = 20
	MaxTransfers = 20
)

// KindStreams is filled by media servers: what is being played right now, how, and what it costs the server.
const KindStreams Kind = "streams"

// How a stream is played.
const (
	PlayDirect       = "direct"       // the client plays the file as it is
	PlayDirectStream = "directstream" // the server repackages it, and may convert the audio, but sends the video as it is
	PlayTranscode    = "transcode"    // the server converts the video
)

// Stream is one session playing something.
type Stream struct {
	User     string  `json:"user"`             // the account, as the app names it
	Client   string  `json:"client"`           // the app it plays in, e.g. a web or TV client
	Device   string  `json:"device,omitempty"` // the device it plays on, as the app names it
	Title    string  `json:"title"`            // e.g. "Series S01E02 · Episode" or "Film (2008)"
	Paused   bool    `json:"paused"`
	Progress float64 `json:"progress"` // 0..1; 0 when the length is unknown
	Method   string  `json:"method"`   // PlayDirect, PlayDirectStream or PlayTranscode; "" if the app does not say
	// Reasons say in plain words why the server converts anything (e.g. "audio codec not supported"); never nil.
	Reasons []string `json:"reasons"`
	// VideoCodec and AudioCodec are the codecs of the file; ToVideoCodec and ToAudioCodec what the server converts
	// them to, empty while a stream is sent as it is.
	VideoCodec   string `json:"videoCodec,omitempty"`
	AudioCodec   string `json:"audioCodec,omitempty"`
	ToVideoCodec string `json:"toVideoCodec,omitempty"`
	ToAudioCodec string `json:"toAudioCodec,omitempty"`
	Bitrate      int64  `json:"bitrate"` // bits/s sent to the client; 0 if unknown
}

// Streams is filled by apps that play media to clients. The counts cover every stream, the list the first ones.
// It never holds a client's network address.
type Streams struct {
	Total      int      `json:"total"`      // sessions playing something, paused ones included
	Paused     int      `json:"paused"`     // of them, paused
	Transcodes int      `json:"transcodes"` // of them, with the video converted (PlayTranscode)
	Bitrate    int64    `json:"bitrate"`    // bits/s of all streams whose bitrate is known
	Streams    []Stream `json:"streams"`    // at most MaxStreams; [] not nil
}

// MaxStreams bounds the streams kept per reading, MaxStreamReasons the reasons kept per stream.
const (
	MaxStreams       = 50
	MaxStreamReasons = 8
)
