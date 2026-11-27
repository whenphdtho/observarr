// Package seerr implements the Observarr adapter for the media request apps that share Overseerr's API: Overseerr,
// Jellyseerr and Seerr, into which both merged with version 3. It reads the app's status and its requests, and
// changes nothing.
//
// The calls and fields below follow the apps' published API documentation (overseerr-api.yml of sct/overseerr and
// seerr-api.yml of seerr-team/seerr, as of November 2026), all under /api/v1:
//
//   - status (no key): {"version", "commitTag", "updateAvailable", "commitsBehind", "restartRequired"}.
//   - status/appdata (no key): {"appData", "appDataPath"}; appData is false when a container keeps its settings
//     outside a mounted volume.
//   - settings/public (no key): the settings the sign-in page needs, among them "applicationTitle". The apps that
//     also serve Jellyfin and Emby add "mediaServerType"; Overseerr has none.
//   - request/count: {"total", "movie", "tv", "pending", "approved", "declined", "processing", "available"}, and
//     "completed" from Seerr on. It has no count of failed requests: request?filter=failed&take=1 gives that, as
//     pageInfo.results.
//   - request?take=&skip=&filter=&sort=: {"pageInfo": {"results", …}, "results": [MediaRequest]}. A MediaRequest
//     has "status" (1 pending approval, 2 approved, 3 declined, 4 failed, 5 completed), "type" ("movie" or "tv"),
//     "is4k", "createdAt", "requestedBy" (a user with "displayName", "username", "plexUsername",
//     "jellyfinUsername" and "email") and "media" ({"tmdbId", "mediaType", "status", "status4k"}, a status being
//     1 unknown, 2 pending, 3 processing, 4 partially available, 5 available). The title is not part of it.
//   - movie/{tmdbId} and tv/{tmdbId}: the title, as "title" for a movie and "name" for a series.
//   - issue/count: {"total", "video", "audio", "subtitles", "others", "open", "closed"}.
//
// Every call but the first three needs the API key (Settings → General), sent as X-Api-Key.
package seerr

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// Type is the adapter's stable id.
const Type = "seerr"

const apiKeyHeader = "X-Api-Key"

const (
	pathStatus       = "/api/v1/status"
	pathAppData      = "/api/v1/status/appdata"
	pathPublic       = "/api/v1/settings/public"
	pathRequestCount = "/api/v1/request/count"
	pathFailed       = "/api/v1/request?take=1&filter=failed"
	pathIssueCount   = "/api/v1/issue/count"
)

// pathLatest lists the newest requests of every state.
var pathLatest = "/api/v1/request?take=" + strconv.Itoa(shape.MaxRequests) + "&skip=0&filter=all&sort=added"

const (
	// titleLookups bounds the title lookups that run at once.
	titleLookups = 2
	// maxTitles bounds the titles the adapter remembers; when full, it forgets them all and starts over.
	maxTitles = 512
	// maxName bounds a requester's name.
	maxName = 128
)

// Adapter is the adapter for Overseerr and the apps sharing its API.
type Adapter struct {
	titles *titleCache
}

// New returns the adapter.
func New() adapter.Adapter { return Adapter{titles: &titleCache{m: map[string]string{}}} }

// Info describes the adapter for the "Add apps" page.
func (Adapter) Info() adapter.Info {
	return adapter.Info{
		Type:        Type,
		Name:        "Overseerr / Jellyseerr",
		DefaultPort: 5055,
		NeedsAPIKey: true,
		Kinds:       []shape.Kind{shape.KindHealth, shape.KindRequests},
		Icon:        "film",
		KeyHint:     "Settings → General → API Key",
	}
}

// status is GET status. The pointers tell a field that is missing from one that is false or empty.
type status struct {
	Version         string  `json:"version"`
	CommitTag       *string `json:"commitTag"`
	UpdateAvailable bool    `json:"updateAvailable"`
	RestartRequired *bool   `json:"restartRequired"`
}

// fetchStatus reads GET status and reports how long it took. Anything that answers but is not one of these apps is
// ErrWrongApp.
func fetchStatus(ctx context.Context, t adapter.Target) (status, time.Duration, error) {
	var s status
	start := time.Now()
	err := adapter.GetJSON(ctx, t, pathStatus, "", &s)
	latency := time.Since(start)
	var code adapter.StatusError
	switch {
	case errors.Is(err, adapter.ErrNotJSON), errors.Is(err, adapter.ErrNotFound),
		errors.As(err, &code) && code < 500:
		return status{}, 0, adapter.ErrWrongApp
	case err != nil:
		return status{}, 0, err
	case s.Version == "" || s.CommitTag == nil || s.RestartRequired == nil:
		return status{}, 0, adapter.ErrWrongApp
	}
	s.Version = shape.Clip(s.Version)
	return s, latency, nil
}

// Detect confirms one of the apps at t and that it accepts the key.
func (Adapter) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	s, _, err := fetchStatus(ctx, t)
	if err != nil {
		return adapter.Identity{}, err
	}
	if _, err := fetchCounts(ctx, t); err != nil {
		return adapter.Identity{}, err
	}
	return adapter.Identity{Version: s.Version}, nil
}

// Fingerprint recognises the apps by GET status, which needs no key. Only a failure to get an answer at all is an
// error; any answer that is not theirs is ok=false.
func (Adapter) Fingerprint(ctx context.Context, t adapter.Target) (bool, string, error) {
	s, _, err := fetchStatus(ctx, t)
	switch _, unreachable := adapter.Describe(err); {
	case err == nil:
		return true, s.Version, nil
	case unreachable && !errors.Is(err, adapter.ErrCrossHostRedirect):
		return false, "", err
	}
	return false, "", nil
}

// MatchesImage recognises the apps' published images.
func (Adapter) MatchesImage(image string) bool {
	switch adapter.ImageRepo(image) {
	case "sctx/overseerr", "linuxserver/overseerr", "lscr.io/linuxserver/overseerr",
		"ghcr.io/linuxserver/overseerr", "hotio/overseerr", "ghcr.io/hotio/overseerr",
		"fallenbagel/jellyseerr", "ghcr.io/fallenbagel/jellyseerr", "ghcr.io/seerr-team/seerr":
		return true
	}
	return false
}

// Probe fills one declared shape kind.
func (a Adapter) Probe(ctx context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	switch kind {
	case shape.KindHealth:
		return probeHealth(ctx, t)
	case shape.KindRequests:
		return a.probeRequests(ctx, t)
	default:
		return nil, adapter.ErrUnknownKind
	}
}

// probeHealth reports the version and latency, and warns of a needed restart, settings that a container would
// lose, and requests waiting for approval or failed. Only status must answer: the requests, if they cannot be
// read, are a warning; status/appdata, which older versions lack, is left out when it cannot be read.
func probeHealth(ctx context.Context, t adapter.Target) (any, error) {
	s, latency, err := fetchStatus(ctx, t)
	if err != nil {
		return nil, err
	}
	h := shape.Health{Version: s.Version, LatencyMS: latency.Milliseconds(), Warnings: []shape.Warning{}}
	warn := func(level shape.Level, msg string) {
		h.Warnings = append(h.Warnings, shape.Warning{Level: level, Message: msg})
	}
	if *s.RestartRequired {
		warn(shape.LevelWarning, "a restart is needed to apply changed settings")
	}
	if s.UpdateAvailable {
		warn(shape.LevelInfo, "an update is available")
	}
	var appData struct {
		AppData *bool `json:"appData"`
	}
	if err := adapter.GetJSON(ctx, t, pathAppData, "", &appData); err == nil && appData.AppData != nil &&
		!*appData.AppData {
		warn(shape.LevelWarning, "the settings are not on a mounted volume: recreating the container loses them")
	}
	c, err := fetchCounts(ctx, t)
	switch {
	case err != nil:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		phrase, _ := adapter.Describe(err)
		warn(shape.LevelWarning, strings.TrimSuffix("the requests cannot be read: "+phrase, ": "))
	default:
		if c.Pending > 0 {
			// Someone waits, nothing is wrong: information, not a warning.
			warn(shape.LevelInfo, plural(c.Pending, "request")+" waiting for approval")
		}
		if c.Failed > 0 {
			warn(shape.LevelWarning, plural(c.Failed, "request")+" failed")
		}
	}
	return h, nil
}

func plural(n int, word string) string {
	s := strconv.Itoa(n) + " " + word
	if n != 1 {
		s += "s"
	}
	return s
}

// counts is GET request/count.
type counts struct {
	Total      int `json:"total"`
	Pending    int `json:"pending"`
	Approved   int `json:"approved"`
	Declined   int `json:"declined"`
	Processing int `json:"processing"`
	Available  int `json:"available"`
}

// fetchCounts reads the request counts, and the failed requests from the request list. An app too old to filter
// by failed requests (400) has none counted.
func fetchCounts(ctx context.Context, t adapter.Target) (shape.RequestCounts, error) {
	var c counts
	if err := adapter.GetJSON(ctx, t, pathRequestCount, apiKeyHeader, &c); err != nil {
		return shape.RequestCounts{}, err
	}
	var failed struct {
		PageInfo struct {
			Results int `json:"results"`
		} `json:"pageInfo"`
	}
	err := adapter.GetJSON(ctx, t, pathFailed, apiKeyHeader, &failed)
	var code adapter.StatusError
	if err != nil && !(errors.As(err, &code) && code == http.StatusBadRequest) {
		return shape.RequestCounts{}, err
	}
	return shape.RequestCounts{
		Total:      max(0, c.Total),
		Pending:    max(0, c.Pending),
		Approved:   max(0, c.Approved),
		Processing: max(0, c.Processing),
		Available:  max(0, c.Available),
		Declined:   max(0, c.Declined),
		Failed:     max(0, failed.PageInfo.Results),
	}, nil
}

// request is one MediaRequest of GET request.
type request struct {
	Status    int    `json:"status"`
	Type      string `json:"type"`
	Is4K      bool   `json:"is4k"`
	CreatedAt string `json:"createdAt"`
	Media     struct {
		TmdbID    int64  `json:"tmdbId"`
		MediaType string `json:"mediaType"`
		Status    int    `json:"status"`
		Status4K  int    `json:"status4k"`
	} `json:"media"`
	// The user's e-mail address is deliberately not decoded.
	RequestedBy struct {
		DisplayName      string `json:"displayName"`
		Username         string `json:"username"`
		PlexUsername     string `json:"plexUsername"`
		JellyfinUsername string `json:"jellyfinUsername"`
	} `json:"requestedBy"`
}

// probeRequests reads the counts, the newest requests with their titles, the open issues and which app answers.
func (a Adapter) probeRequests(ctx context.Context, t adapter.Target) (any, error) {
	c, err := fetchCounts(ctx, t)
	if err != nil {
		return nil, err
	}
	var list struct {
		Results []request `json:"results"`
	}
	if err := adapter.GetJSON(ctx, t, pathLatest, apiKeyHeader, &list); err != nil {
		return nil, err
	}
	latest := list.Results[:min(len(list.Results), shape.MaxRequests)]
	out := shape.Requests{Counts: c, Latest: make([]shape.MediaRequest, len(latest))}
	for i, r := range latest {
		out.Latest[i] = shape.MediaRequest{Type: mediaType(r), RequestedBy: requester(r), State: state(r)}
		if at, err := time.Parse(time.RFC3339, r.CreatedAt); err == nil {
			out.Latest[i].At = at.UTC()
		}
	}
	if err := a.fillTitles(ctx, t, latest, out.Latest); err != nil {
		return nil, err
	}

	var issues struct {
		Open *int `json:"open"`
	}
	switch err := adapter.GetJSON(ctx, t, pathIssueCount, apiKeyHeader, &issues); {
	case errors.Is(err, adapter.ErrNotFound):
	case err != nil:
		return nil, err
	case issues.Open != nil:
		open := max(0, *issues.Open)
		out.OpenIssues = &open
	}

	out.App, err = appName(ctx, t)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// appName tells the apps apart: Seerr from version 3 on, Jellyseerr by the media server setting Overseerr lacks.
func appName(ctx context.Context, t adapter.Target) (string, error) {
	s, _, err := fetchStatus(ctx, t)
	if err != nil {
		return "", err
	}
	if major, _, _ := strings.Cut(s.Version, "."); atLeast(major, 3) {
		return "Seerr", nil
	}
	var public struct {
		MediaServerType json.RawMessage `json:"mediaServerType"`
	}
	if err := adapter.GetJSON(ctx, t, pathPublic, "", &public); err != nil {
		return "", err
	}
	if len(public.MediaServerType) > 0 && string(public.MediaServerType) != "null" {
		return "Jellyseerr", nil
	}
	return "Overseerr", nil
}

// atLeast reports whether s is a whole number of at least n.
func atLeast(s string, n int) bool {
	v, err := strconv.Atoi(s)
	return err == nil && v >= n
}

// mediaType is "movie" or "series", or "" when the request says neither.
func mediaType(r request) string {
	typ := r.Type
	if typ == "" {
		typ = r.Media.MediaType
	}
	switch typ {
	case "movie":
		return "movie"
	case "tv":
		return "series"
	}
	return ""
}

// requester is the name the app shows for the user who made r. The app falls back to the e-mail address for a
// user without a name; that is never passed on.
func requester(r request) string {
	u := r.RequestedBy
	for _, name := range []string{u.DisplayName, u.Username, u.PlexUsername, u.JellyfinUsername} {
		if name = strings.TrimSpace(name); name != "" && !strings.Contains(name, "@") {
			return shape.ClipTo(name, maxName)
		}
	}
	return ""
}

// The app's request and media states.
const (
	reqPending   = 1
	reqApproved  = 2
	reqDeclined  = 3
	reqFailed    = 4
	reqCompleted = 5

	mediaProcessing = 3
	mediaPartial    = 4
	mediaAvailable  = 5
)

// state is r's state, as the app's own request list shows it: the request's own state until it is approved, then
// how far the media got.
func state(r request) string {
	switch r.Status {
	case reqPending:
		return shape.RequestPending
	case reqDeclined:
		return shape.RequestDeclined
	case reqFailed:
		return shape.RequestFailed
	}
	media := r.Media.Status
	if r.Is4K {
		media = r.Media.Status4K
	}
	switch {
	case media == mediaAvailable:
		return shape.RequestAvailable
	case media == mediaPartial:
		return shape.RequestPartial
	case r.Status == reqCompleted:
		return shape.RequestCompleted
	case media == mediaProcessing:
		return shape.RequestProcessing
	}
	return shape.RequestApproved
}

// fillTitles sets the title of each of out from the app's media details, at most titleLookups at a time, and
// remembers them. A title that cannot be looked up is named by its TMDB id.
func (a Adapter) fillTitles(ctx context.Context, t adapter.Target, reqs []request, out []shape.MediaRequest) error {
	sem := make(chan struct{}, titleLookups)
	var wg sync.WaitGroup
	for i, r := range reqs {
		path := detailsPath(out[i].Type, r.Media.TmdbID)
		if path == "" {
			out[i].Title = "Unknown title"
			continue
		}
		key := t.BaseURL.String() + path
		if title, ok := a.titles.get(key); ok {
			out[i].Title = title
			continue
		}
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			var details struct {
				Title string `json:"title"`
				Name  string `json:"name"`
			}
			if err := adapter.GetJSON(ctx, t, path, apiKeyHeader, &details); err == nil {
				if title := shape.Clip(strings.TrimSpace(cmp.Or(details.Title, details.Name))); title != "" {
					out[i].Title = title
					a.titles.put(key, title)
					return
				}
			}
			out[i].Title = "TMDB " + strconv.FormatInt(r.Media.TmdbID, 10)
		})
	}
	wg.Wait()
	return ctx.Err()
}

// detailsPath is where the app serves the details of the media typ with TMDB id, or "" for none.
func detailsPath(typ string, id int64) string {
	if id <= 0 {
		return ""
	}
	switch typ {
	case "movie":
		return "/api/v1/movie/" + strconv.FormatInt(id, 10)
	case "series":
		return "/api/v1/tv/" + strconv.FormatInt(id, 10)
	}
	return ""
}

// titleCache remembers titles by where they were looked up, at most maxTitles of them.
type titleCache struct {
	mu sync.Mutex
	m  map[string]string
}

func (c *titleCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	title, ok := c.m[key]
	return title, ok
}

func (c *titleCache) put(key, title string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxTitles {
		clear(c.m)
	}
	c.m[key] = title
}
