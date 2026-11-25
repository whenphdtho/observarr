package usenet

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// SABnzbd's API is GET /api?mode=…&output=json&apikey=…, the key a query parameter. The calls below were checked
// against SABnzbd's own source (github.com/sabnzbd/sabnzbd, sabnzbd/api.py and interface.py, November 2026):
//
//   - mode=version: {"version": "4.5.1"}. Needs no key.
//   - mode=queue (build_queue): {"queue": {"paused", "kbpersec" ("1234.56", KiB/s), "diskspace1" and
//     "diskspace2" (free GiB in the incomplete and complete folders, as text), "noofslots_total", "slots": [{
//     "filename", "status" ("Downloading", "Queued", "Paused", …), "mb", "mbleft" (MiB, as text), "percentage",
//     "timeleft" ("0:12:34")}]}}. "limit" bounds the slots.
//   - mode=history&failed_only=1 (build_history): {"history": {"slots": [{"name", "status", "fail_message",
//     "completed" (unix time)}]}}, newest first.
//   - mode=warnings: {"warnings": [{"type" ("WARNING", "ERROR"), "text", "time" (unix time)}]}, oldest first;
//     the list SABnzbd's interface shows until someone clears it.
//
// A wrong key is a 403 in current versions and, in older ones, an answer of {"status": false, "error": "API Key
// Incorrect"}.

// The calls the SABnzbd adapter makes. Each only reads: none carries a "name", which turns a mode into an action
// (mode=queue&name=delete deletes downloads, mode=warnings&name=clear clears the warnings).
var (
	sabVersion  = url.Values{"mode": {"version"}}
	sabStatus   = url.Values{"mode": {"queue"}, "limit": {"1"}}
	sabQueue    = url.Values{"mode": {"queue"}, "limit": {strconv.Itoa(shape.MaxQueueItems)}}
	sabFailed   = url.Values{"mode": {"history"}, "failed_only": {"1"}, "limit": {strconv.Itoa(sabFailedLimit)}}
	sabWarnings = url.Values{"mode": {"warnings"}}
)

// sabFailedLimit bounds the failed downloads a health reading asks for.
const sabFailedLimit = 20

// sabMinMajor is the oldest supported major version: warnings are objects with a time since SABnzbd 3.
const sabMinMajor = 3

type sabnzbd struct{}

// NewSABnzbd returns the SABnzbd adapter.
func NewSABnzbd() adapter.Adapter { return sabnzbd{} }

// Info describes SABnzbd for the UI's "Add apps" page.
func (sabnzbd) Info() adapter.Info {
	return adapter.Info{
		Type:        "sabnzbd",
		Name:        "SABnzbd",
		DefaultPort: 8080,
		NeedsAPIKey: true,
		Kinds:       []shape.Kind{shape.KindHealth, shape.KindQueue},
		Icon:        "download",
		KeyHint:     "the API Key under Config → General → Security",
	}
}

// Logo returns SABnzbd's logo.
func (sabnzbd) Logo() []byte { return logo("sabnzbd") }

// sabAnswer is any answer of SABnzbd's API, which can carry an error in place of its data.
type sabAnswer interface{ failure() error }

// sabError is how SABnzbd refuses a call in the answer itself: {"status": false, "error": "…"}.
type sabError struct {
	Error string `json:"error"`
}

var errSABRefused = errors.New("SABnzbd refused the request")

// errSABRequest is a failed request whose error would have repeated the key.
var errSABRequest = errors.New("sabnzbd: request failed")

func (e sabError) failure() error {
	switch {
	case e.Error == "":
		return nil
	case strings.Contains(strings.ToLower(e.Error), "api key"):
		return adapter.ErrUnauthorized
	}
	return errSABRefused // not e.Error itself: the app's text never reaches an error
}

// sabGet makes one of the calls above and decodes its answer into v. The key travels in the query, so it is
// taken out of any error that carries the request URL (see withoutKey).
func sabGet(ctx context.Context, t adapter.Target, call url.Values, v sabAnswer) error {
	q := maps.Clone(call)
	q.Set("output", "json")
	if t.APIKey != "" {
		q.Set("apikey", t.APIKey)
	}
	if err := adapter.GetJSON(ctx, t, "/api?"+q.Encode(), "", v); err != nil {
		return withoutKey(err, t.APIKey)
	}
	return v.failure()
}

// withoutKey returns err without the key: net/http's errors name the request URL (or the one a redirect led to),
// whose query holds the key, and their causes may repeat it (a malformed redirect's Location, say). A *url.Error is
// rebuilt afresh, with the query cut from its URL and its cause reduced to adapter.BareCause; the error it was
// wrapped in held that text too, so that goes. Should the key still show, only a fixed error is left.
func withoutKey(err error, key string) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		u, _, _ := strings.Cut(ue.URL, "?")
		err = fmt.Errorf("sabnzbd: request failed: %w", &url.Error{Op: ue.Op, URL: u, Err: adapter.BareCause(ue.Err)})
	}
	if key != "" && (strings.Contains(err.Error(), key) || strings.Contains(err.Error(), url.QueryEscape(key))) {
		return errSABRequest
	}
	return err
}

type sabVersionAnswer struct {
	sabError
	Version string `json:"version"`
}

func fetchSABVersion(ctx context.Context, t adapter.Target) (string, time.Duration, error) {
	var v sabVersionAnswer
	start := time.Now()
	err := sabGet(ctx, t, sabVersion, &v)
	return v.Version, time.Since(start), err
}

// Detect confirms that SABnzbd answers at t (by mode=version) and accepts the key (by a one-slot queue).
func (sabnzbd) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	version, _, err := fetchSABVersion(ctx, t)
	if err != nil {
		// Another app answers /api with a 404 or a page that is not JSON.
		if errors.Is(err, adapter.ErrNotJSON) || errors.Is(err, adapter.ErrNotFound) {
			return adapter.Identity{}, adapter.ErrWrongApp
		}
		return adapter.Identity{}, err
	}
	if version == "" {
		return adapter.Identity{}, adapter.ErrWrongApp
	}
	if major, err := strconv.Atoi(strings.SplitN(version, ".", 2)[0]); err == nil && major < sabMinMajor {
		return adapter.Identity{}, adapter.ErrUnsupported
	}
	if err := sabGet(ctx, t, sabStatus, &sabQueueAnswer{}); err != nil {
		return adapter.Identity{}, err
	}
	return adapter.Identity{Version: shape.Clip(version)}, nil
}

// Fingerprint recognises SABnzbd without a key: mode=version needs none.
func (sabnzbd) Fingerprint(ctx context.Context, t adapter.Target) (bool, string, error) {
	t.APIKey = ""
	version, _, err := fetchSABVersion(ctx, t)
	if err != nil {
		return false, "", fingerprintErr(err)
	}
	return version != "", shape.Clip(version), nil
}

// Probe fills one declared shape kind.
func (sabnzbd) Probe(ctx context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	switch kind {
	case shape.KindHealth:
		return probeSABHealth(ctx, t)
	case shape.KindQueue:
		return probeSABQueue(ctx, t)
	default:
		return nil, adapter.ErrUnknownKind
	}
}

// sabNumber is a figure SABnzbd sends as text ("1234.56"), read whether it comes as text or as a number. Anything
// else reads as 0 rather than failing the whole answer.
type sabNumber float64

func (n *sabNumber) UnmarshalJSON(b []byte) error {
	f, err := strconv.ParseFloat(strings.Trim(string(b), `"`), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		f = 0
	}
	*n = sabNumber(f)
	return nil
}

// bytes is n, a figure in units of unit bytes, in bytes: at least 0 and at most 2^62.
func (n sabNumber) bytes(unit float64) int64 {
	return int64(min(max(float64(n)*unit, 0), 1<<62))
}

type sabSlot struct {
	Filename string    `json:"filename"`
	Status   string    `json:"status"`
	MB       sabNumber `json:"mb"`
	MBLeft   sabNumber `json:"mbleft"`
	TimeLeft string    `json:"timeleft"`
}

type sabQueueAnswer struct {
	sabError
	Queue struct {
		Paused     bool      `json:"paused"`
		KBPerSec   sabNumber `json:"kbpersec"`
		DiskSpace1 *string   `json:"diskspace1"`
		DiskSpace2 *string   `json:"diskspace2"`
		Total      int       `json:"noofslots_total"`
		Slots      []sabSlot `json:"slots"`
	} `json:"queue"`
}

// freeBytes is the least free space SABnzbd reports for its two download folders, or -1 if it reports none.
func (a *sabQueueAnswer) freeBytes() int64 {
	free := int64(-1)
	for _, s := range []*string{a.Queue.DiskSpace1, a.Queue.DiskSpace2} {
		if s == nil {
			continue
		}
		gib, err := strconv.ParseFloat(*s, 64)
		if err != nil || gib < 0 {
			continue
		}
		if b := sabNumber(gib).bytes(1 << 30); free < 0 || b < free {
			free = b
		}
	}
	return free
}

func probeSABQueue(ctx context.Context, t adapter.Target) (any, error) {
	var a sabQueueAnswer
	if err := sabGet(ctx, t, sabQueue, &a); err != nil {
		return nil, err
	}
	q := shape.Queue{
		Items:  make([]shape.QueueItem, 0, min(len(a.Queue.Slots), shape.MaxQueueItems)),
		Speed:  a.Queue.KBPerSec.bytes(1 << 10),
		Paused: a.Queue.Paused,
	}
	for _, s := range a.Queue.Slots[:min(len(a.Queue.Slots), shape.MaxQueueItems)] {
		size, left := s.MB.bytes(1<<20), s.MBLeft.bytes(1<<20)
		item := shape.QueueItem{
			Title:     shape.Clip(s.Filename),
			Status:    shape.Clip(strings.ToLower(s.Status)),
			Progress:  progress(float64(size), float64(left)),
			SizeBytes: size,
			LeftBytes: left,
		}
		// SABnzbd says "0:00:00" for an item it is not downloading.
		if s.TimeLeft != "0:00:00" {
			item.ETA = shape.Clip(s.TimeLeft)
		}
		q.Items = append(q.Items, item)
	}
	q.Total = max(a.Queue.Total, len(q.Items))
	return q, nil
}

type sabWarningsAnswer struct {
	sabError
	Warnings []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Time int64  `json:"time"`
	} `json:"warnings"`
}

type sabHistoryAnswer struct {
	sabError
	History struct {
		Slots []struct {
			Name        string `json:"name"`
			FailMessage string `json:"fail_message"`
			Completed   int64  `json:"completed"`
		} `json:"slots"`
	} `json:"history"`
}

// probeSABHealth reports the version and latency, a paused downloader, low disk space, the warnings of the last day
// (newest first) and the downloads that failed in it.
func probeSABHealth(ctx context.Context, t adapter.Target) (any, error) {
	version, latency, err := fetchSABVersion(ctx, t)
	if err != nil {
		return nil, err
	}
	var (
		status   sabQueueAnswer
		warnings sabWarningsAnswer
		history  sabHistoryAnswer
	)
	for _, c := range []struct {
		call url.Values
		into sabAnswer
	}{{sabStatus, &status}, {sabWarnings, &warnings}, {sabFailed, &history}} {
		if err := sabGet(ctx, t, c.call, c.into); err != nil {
			return nil, err
		}
	}

	now := time.Now()
	h := shape.Health{Version: shape.Clip(version), LatencyMS: latency.Milliseconds(), Warnings: []shape.Warning{}}
	if status.Queue.Paused {
		h.Warnings = append(h.Warnings, pausedWarning)
	}
	if w, ok := diskWarning(status.freeBytes()); ok {
		h.Warnings = append(h.Warnings, w)
	}
	failed := newFailures(now)
	for _, s := range history.History.Slots {
		if !failed.add(s.Completed, shape.Clip(s.Name), shape.Clip(firstLine(s.FailMessage))) {
			break
		}
	}
	failed.atLeast = failed.n >= sabFailedLimit
	if w, ok := failed.warning(); ok {
		h.Warnings = append(h.Warnings, w)
	}
	cutoff := now.Add(-failedWindow).Unix()
	for _, w := range slices.Backward(warnings.Warnings) {
		if len(h.Warnings) >= shape.MaxWarnings || w.Time < cutoff {
			break
		}
		level := shape.LevelInfo
		switch strings.ToUpper(w.Type) {
		case "ERROR", "CRITICAL":
			level = shape.LevelError
		case "WARNING":
			level = shape.LevelWarning
		}
		h.Warnings = append(h.Warnings, shape.Warning{Level: level, Message: shape.Clip(firstLine(w.Text))})
	}
	return h, nil
}

// MatchesImage recognises SABnzbd's own image and the LinuxServer.io and hotio builds.
func (sabnzbd) MatchesImage(image string) bool {
	switch adapter.ImageRepo(image) {
	case "sabnzbd/sabnzbd", "linuxserver/sabnzbd", "lscr.io/linuxserver/sabnzbd", "ghcr.io/linuxserver/sabnzbd",
		"hotio/sabnzbd", "ghcr.io/hotio/sabnzbd":
		return true
	}
	return false
}

// ReadConfig reads SABnzbd's sabnzbd.ini from its config folder: in its [misc] section, api_key, port, https_port
// with enable_https, and url_base. The host is left for the user (or a matching container) to complete.
func (sabnzbd) ReadConfig(name string, dir fs.FS) (adapter.Candidate, bool, error) {
	const file = "sabnzbd.ini"
	body, err := adapter.ReadConfigFile(dir, file)
	if errors.Is(err, fs.ErrNotExist) {
		return adapter.Candidate{}, false, nil
	}
	if err != nil {
		return adapter.Candidate{}, false, err
	}
	misc := configValues(body, "misc", "api_key", "port", "https_port", "enable_https", "url_base")
	scheme, port := "http", configPort(misc["port"])
	if misc["enable_https"] == "1" {
		// Without an https_port of its own, SABnzbd serves HTTPS on its port.
		scheme, port = "https", cmp.Or(configPort(misc["https_port"]), port)
	}
	u := &url.URL{Scheme: scheme, Path: "/" + strings.Trim(misc["url_base"], "/")}
	if port != 0 {
		u.Host = ":" + strconv.Itoa(port)
	}
	return adapter.Candidate{Type: "sabnzbd", Name: "SABnzbd", BaseURL: u, APIKey: misc["api_key"],
		KeySource: file + " in " + name}, true, nil
}
