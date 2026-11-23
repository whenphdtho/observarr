// Package arr implements one Observarr adapter shared by the whole *arr family (Sonarr, Radarr,
// Lidarr, Prowlarr): they all speak the same API shape, differing only in app name, API version
// and port, which Definition captures.
package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// Definition configures the shared *arr adapter for one app of the family.
type Definition struct {
	Type, Name  string
	AppName     string // value of appName in /system/status, e.g. "Sonarr"
	APIVersion  string // "v3" or "v1"
	DefaultPort int
	MinMajor    int // oldest supported major version
	HasQueue    bool
	QueueQuery  string // extra query parameters for /queue, e.g. "&includeUnknownSeriesItems=true"
	Links       bool   // true if this app reports other apps it is configured against (adapter.Linker); only Prowlarr does
}

// Definitions lists the supported family members. Readarr is not included: it was retired
// upstream in 2025 and is not part of the supported app list.
var Definitions = []Definition{
	{Type: "sonarr", Name: "Sonarr", AppName: "Sonarr", APIVersion: "v3", DefaultPort: 8989, MinMajor: 3, HasQueue: true,
		QueueQuery: "&includeUnknownSeriesItems=true"},
	{Type: "radarr", Name: "Radarr", AppName: "Radarr", APIVersion: "v3", DefaultPort: 7878, MinMajor: 3, HasQueue: true},
	{Type: "lidarr", Name: "Lidarr", AppName: "Lidarr", APIVersion: "v1", DefaultPort: 8686, MinMajor: 1, HasQueue: true},
	{Type: "prowlarr", Name: "Prowlarr", AppName: "Prowlarr", APIVersion: "v1", DefaultPort: 9696, MinMajor: 1, Links: true},
}

// apiKeyHeader is the header every *arr app expects its API key on.
const apiKeyHeader = "X-Api-Key"

// arrAdapter is the adapter.Adapter for one *arr Definition.
type arrAdapter struct{ def Definition }

// New returns the adapter for d. A family member whose Definition sets Links (only Prowlarr,
// currently) gets a distinct concrete type that also implements adapter.Linker, so a type
// assertion on the returned value sees Linker only for that member: arrAdapter itself declares no
// LinkedApps method, precisely so Sonarr/Radarr/Lidarr never satisfy adapter.Linker merely by
// sharing this package with Prowlarr.
func New(d Definition) adapter.Adapter {
	base := arrAdapter{def: d}
	if d.Links {
		return linkingArrAdapter{base}
	}
	return base
}

// Info describes this family member for the UI's "Add service" form.
func (a arrAdapter) Info() adapter.Info {
	kinds := []shape.Kind{shape.KindHealth}
	if a.def.HasQueue {
		kinds = append(kinds, shape.KindQueue)
	}
	return adapter.Info{
		Type:        a.def.Type,
		Name:        a.def.Name,
		DefaultPort: a.def.DefaultPort,
		NeedsAPIKey: true,
		Kinds:       kinds,
	}
}

// statusResponse is the relevant subset of GET /api/{v}/system/status.
type statusResponse struct {
	AppName string `json:"appName"`
	Version string `json:"version"`
}

// path builds "/api/<version><suffix>".
func (a arrAdapter) path(suffix string) string {
	return "/api/" + a.def.APIVersion + suffix
}

// fetchStatus calls /system/status and reports its own call latency, regardless of whether the
// answer actually looks like this app (that check is Detect's job).
func (a arrAdapter) fetchStatus(ctx context.Context, t adapter.Target) (statusResponse, time.Duration, error) {
	var status statusResponse
	start := time.Now()
	err := adapter.GetJSON(ctx, t, a.path("/system/status"), apiKeyHeader, &status)
	return status, time.Since(start), err
}

// leadingMajor returns the integer before the first '.' in version (or all of version if there is
// none). It errors if that prefix is not a valid integer.
func leadingMajor(version string) (int, error) {
	s := version
	if i := strings.IndexByte(version, '.'); i >= 0 {
		s = version[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("arr: version %q has no leading integer", version)
	}
	return n, nil
}

// Detect confirms that t is really this family member, by its real status endpoint, and returns
// its version.
func (a arrAdapter) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	status, _, err := a.fetchStatus(ctx, t)
	if err != nil {
		// A 404 or a non-JSON answer (e.g. a login page) means something other than an *arr
		// app is at this address, not that this *arr app is unreachable.
		if errors.Is(err, adapter.ErrNotJSON) || errors.Is(err, adapter.ErrNotFound) {
			return adapter.Identity{}, adapter.ErrWrongApp
		}
		return adapter.Identity{}, err
	}
	// Covers a different app, a missing appName field, and a JSON "null" body alike: all
	// leave AppName at its zero value, which never case-insensitively equals a real app name.
	if !strings.EqualFold(status.AppName, a.def.AppName) {
		return adapter.Identity{}, adapter.WrongApp(status.AppName)
	}

	major, err := leadingMajor(status.Version)
	if err != nil || major < a.def.MinMajor {
		return adapter.Identity{}, adapter.ErrUnsupported
	}
	return adapter.Identity{Version: shape.Clip(status.Version)}, nil
}

// healthCheck is one entry of GET /api/{v}/health.
type healthCheck struct {
	Source  string `json:"source"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func levelFor(checkType string) shape.Level {
	switch strings.ToLower(checkType) {
	case "error":
		return shape.LevelError
	case "warning":
		return shape.LevelWarning
	default:
		return shape.LevelInfo
	}
}

func (a arrAdapter) probeHealth(ctx context.Context, t adapter.Target) (any, error) {
	status, latency, err := a.fetchStatus(ctx, t)
	if err != nil {
		return nil, err
	}

	warnings := []shape.Warning{}
	err = adapter.StreamJSON(ctx, t, a.path("/health"), apiKeyHeader, func(d *json.Decoder) error {
		_, err := adapter.DecodeArray(d, shape.MaxWarnings, func(c healthCheck) {
			warnings = append(warnings, shape.Warning{
				Level:   levelFor(c.Type),
				Message: shape.Clip(c.Message),
				Source:  shape.Clip(c.Source),
			})
		})
		return err
	})
	if err != nil {
		return nil, err
	}

	return shape.Health{
		Version:   shape.Clip(status.Version),
		LatencyMS: latency.Milliseconds(),
		Warnings:  warnings,
	}, nil
}

// queueStatusMessage is one entry of a queue record's statusMessages.
type queueStatusMessage struct {
	Messages []string `json:"messages"`
}

// queueRecord is one entry of GET /api/{v}/queue's records. Size and SizeLeft decode as float64
// (not int64) because some apps report them with a fractional part; a value like "size": 100.0
// must not fail the whole queue.
type queueRecord struct {
	Title                 string               `json:"title"`
	Status                string               `json:"status"`
	Size                  float64              `json:"size"`
	SizeLeft              float64              `json:"sizeleft"`
	TimeLeft              string               `json:"timeleft"`
	ErrorMessage          string               `json:"errorMessage"`
	TrackedDownloadStatus string               `json:"trackedDownloadStatus"`
	StatusMessages        []queueStatusMessage `json:"statusMessages"`
}

func mapQueueItem(r queueRecord) shape.QueueItem {
	var progress float64
	if r.Size > 0 {
		progress = (r.Size - r.SizeLeft) / r.Size
	}
	progress = clampUnit(progress)

	problem := r.ErrorMessage
	if problem == "" {
		status := strings.ToLower(r.TrackedDownloadStatus)
		if status == "warning" || status == "error" {
			var messages []string
			for _, sm := range r.StatusMessages {
				messages = append(messages, sm.Messages...)
			}
			problem = strings.Join(messages, "; ")
		}
	}

	return shape.QueueItem{
		Title:     shape.Clip(r.Title),
		Status:    shape.Clip(strings.ToLower(r.Status)),
		Progress:  progress,
		SizeBytes: int64(r.Size),
		LeftBytes: int64(r.SizeLeft),
		ETA:       shape.Clip(r.TimeLeft),
		Problem:   shape.Clip(problem),
	}
}

// clampUnit confines p to [0, 1]: an app can report sizeleft outside [0, size] (a negative
// sizeleft, or one larger than size), which would otherwise make Progress misleading or, in the
// UI, overflow a progress bar.
func clampUnit(p float64) float64 {
	switch {
	case p < 0:
		return 0
	case p > 1:
		return 1
	default:
		return p
	}
}

func (a arrAdapter) probeQueue(ctx context.Context, t adapter.Target) (any, error) {
	suffix := fmt.Sprintf("/queue?page=1&pageSize=%d%s", shape.MaxQueueItems, a.def.QueueQuery)
	q := shape.Queue{Items: []shape.QueueItem{}}
	err := adapter.StreamJSON(ctx, t, a.path(suffix), apiKeyHeader, func(d *json.Decoder) error {
		return decodeQueue(d, &q)
	})
	if err != nil {
		return nil, err
	}
	q.Total = max(q.Total, len(q.Items))
	return q, nil
}

// decodeQueue reads the relevant fields of GET /api/{v}/queue's answer, an object (or null) with totalRecords and
// records, into q. It stops reading at the first MaxQueueItems records, so an answer with more than the page asked
// for is never read in full.
func decodeQueue(d *json.Decoder, q *shape.Queue) error {
	tok, err := d.Token()
	if err != nil || tok == nil {
		return err
	}
	if tok != json.Delim('{') {
		return errors.New("not a JSON object")
	}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return err
		}
		name, _ := key.(string)
		switch {
		case strings.EqualFold(name, "totalRecords"):
			err = d.Decode(&q.Total)
		case strings.EqualFold(name, "records"):
			var capped bool
			capped, err = adapter.DecodeArray(d, shape.MaxQueueItems, func(r queueRecord) {
				q.Items = append(q.Items, mapQueueItem(r))
			})
			if capped {
				return nil
			}
		default:
			err = d.Decode(&json.RawMessage{})
		}
		if err != nil {
			return err
		}
	}
	_, err = d.Token() // the closing brace
	return err
}

// Probe fills one declared shape kind.
func (a arrAdapter) Probe(ctx context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	switch kind {
	case shape.KindHealth:
		return a.probeHealth(ctx, t)
	case shape.KindQueue:
		if !a.def.HasQueue {
			return nil, adapter.ErrUnknownKind
		}
		return a.probeQueue(ctx, t)
	default:
		return nil, adapter.ErrUnknownKind
	}
}
