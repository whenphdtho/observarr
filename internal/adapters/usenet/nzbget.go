package usenet

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// NZBGet's API is JSON-RPC: a POST to /jsonrpc of {"method", "params"}, answered with {"version": "1.1", "result":
// …} or {"version": "1.1", "error": {"name", "code", "message"}}, "result" or "error" last. Every call needs the
// control user's HTTP basic authentication when a ControlPassword is set. The calls below were checked against
// NZBGet's own source (github.com/nzbgetcom/nzbget, daemon/remote/XmlRpc.cpp and WebServer.cpp, November 2026):
//
//   - version: the version as text, "24.3".
//   - status: among others DownloadRateLo/Hi (bytes/s), DownloadPaused, ServerStandBy, QuotaReached and
//     FreeDiskSpaceLo/Hi (bytes free in the destination folder). A 64-bit figure comes as two 32-bit halves;
//     the deprecated single fields (DownloadRate, FreeDiskSpaceMB) overflow on large values.
//   - listgroups(0): the queue in download order, each {"NZBName", "Status" ("QUEUED", "PAUSED", "DOWNLOADING",
//     "FETCHING", or a post-processing stage such as "REPAIRING" or "UNPACKING"), "FileSizeLo/Hi",
//     "RemainingSizeLo/Hi", "PausedSizeLo/Hi", "Health" and "CriticalHealth" (per mille)}, and much more.
//   - history(false): the history newest first, each {"Name", "Status" ("SUCCESS/ALL", "FAILURE/PAR",
//     "DELETED/MANUAL", …), "HistoryTime" (unix time)}, and much more. It has no limit: every item, however many,
//     is sent; this adapter reads only as far as it needs.
//
// Every response, even a refusal, names the app in its Server header: "nzbget-24.3".

// nzbPath is NZBGet's JSON-RPC endpoint.
const nzbPath = "/jsonrpc"

// The JSON-RPC methods the NZBGet adapter calls. None changes anything.
const (
	nzbMethodVersion = "version"
	nzbMethodStatus  = "status"
	nzbMethodGroups  = "listgroups"
	nzbMethodHistory = "history"
)

// nzbMethods is every method the adapter may call; nzbCall refuses any other.
var nzbMethods = []string{nzbMethodVersion, nzbMethodStatus, nzbMethodGroups, nzbMethodHistory}

// errNotAllowed is a programming error: a call outside nzbMethods.
var errNotAllowed = errors.New("nzbget: method not allowed")

var (
	errNZBRefused = errors.New("NZBGet refused the request")
	errNoResult   = errors.New("the answer holds no result")
	// errEnough ends reading an answer early, once it has given what was needed.
	errEnough = errors.New("enough read")
)

// maxHistoryRead bounds the history items a health reading reads looking for the last day's failures.
const maxHistoryRead = 200

const (
	// nzbAnswerBytes bounds an answer of NZBGet's.
	nzbAnswerBytes = 4 << 20
	// nzbQueueBytes bounds the queue listing, which is read as a stream and kept only up to shape.MaxQueueItems:
	// at about 2 KiB an item, room for some 16,000 items.
	nzbQueueBytes = 32 << 20
	// maxBytes caps a byte figure, and the sums of them the queue keeps, far below overflow.
	maxBytes = 1 << 62
)

type nzbget struct{}

// NewNZBGet returns the NZBGet adapter.
func NewNZBGet() adapter.Adapter { return nzbget{} }

// Info describes NZBGet for the UI's "Add apps" page.
func (nzbget) Info() adapter.Info {
	return adapter.Info{
		Type:        "nzbget",
		Name:        "NZBGet",
		DefaultPort: 6789,
		NeedsAPIKey: false,
		Kinds:       []shape.Kind{shape.KindHealth, shape.KindQueue},
		Icon:        "download",
		KeyLabel:    "Credentials",
		KeyHint:     "user:password of ControlUsername/ControlPassword; leave blank if no password is set",
	}
}

// Logo returns NZBGet's logo.
func (nzbget) Logo() []byte { return logo("nzbget") }

// nzbCall calls one allowed method with params and hands its result, of an answer of at most limit bytes, to read.
// read may return errEnough to stop reading early; nzbCall then succeeds.
func nzbCall(ctx context.Context, t adapter.Target, method string, params []any, limit int64,
	read func(*json.Decoder) error) error {
	if !slices.Contains(nzbMethods, method) {
		return fmt.Errorf("%w: %s", errNotAllowed, method)
	}
	authorize, err := adapter.BasicAuth(t.APIKey)
	if err != nil {
		return err
	}
	if params == nil {
		params = []any{}
	}
	body, err := json.Marshal(map[string]any{"version": "1.1", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	var refused, answered bool
	err = adapter.PostJSONLimit(ctx, t, nzbPath, body, authorize, limit, func(d *json.Decoder) error {
		err := adapter.DecodeObject(d, map[string]func(*json.Decoder) error{
			"result": func(d *json.Decoder) error { answered = true; return read(d) },
			"error": func(d *json.Decoder) error {
				var e json.RawMessage
				refused = true
				return d.Decode(&e)
			},
		})
		if errors.Is(err, errEnough) {
			return nil
		}
		return err
	})
	switch {
	case err != nil:
		return err
	case refused:
		return errNZBRefused // not the app's message: its text never reaches an error
	case !answered:
		return errNoResult
	}
	return nil
}

// nzbCallInto is nzbCall decoding the whole result into v.
func nzbCallInto(ctx context.Context, t adapter.Target, method string, v any) error {
	return nzbCall(ctx, t, method, nil, nzbAnswerBytes, func(d *json.Decoder) error { return d.Decode(v) })
}

// join is the 64-bit figure NZBGet sends as two 32-bit halves, at most maxBytes.
func join(hi, lo uint32) int64 { return int64(min(uint64(hi)<<32|uint64(lo), maxBytes)) }

func fetchNZBVersion(ctx context.Context, t adapter.Target) (string, time.Duration, error) {
	var v string
	start := time.Now()
	err := nzbCallInto(ctx, t, nzbMethodVersion, &v)
	return v, time.Since(start), err
}

// Detect confirms that NZBGet answers at t, with the credentials if any, by its version method.
func (nzbget) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	if _, err := adapter.BasicAuth(t.APIKey); err != nil {
		return adapter.Identity{}, err
	}
	version, _, err := fetchNZBVersion(ctx, t)
	if err != nil {
		// Another app answers a POST to /jsonrpc with a 404, a 405 or 501 (no POST), a page that is not JSON, or JSON
		// of another form.
		var status adapter.StatusError
		if errors.Is(err, adapter.ErrNotJSON) || errors.Is(err, adapter.ErrNotFound) || errors.Is(err, errNoResult) ||
			errors.As(err, &status) && (status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented) {
			return adapter.Identity{}, adapter.ErrWrongApp
		}
		return adapter.Identity{}, err
	}
	if version == "" {
		return adapter.Identity{}, adapter.ErrWrongApp
	}
	return adapter.Identity{Version: shape.Clip(version)}, nil
}

// Fingerprint recognises NZBGet without credentials by the Server header it sends with every answer, a refusal
// included.
func (nzbget) Fingerprint(ctx context.Context, t adapter.Target) (bool, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.BaseURL.JoinPath(nzbPath).String(), nil)
	if err != nil {
		return false, "", err
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return false, "", fingerprintErr(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	version, ok := strings.CutPrefix(resp.Header.Get("Server"), "nzbget-")
	if !ok {
		return false, "", nil
	}
	return true, shape.Clip(version), nil
}

// Probe fills one declared shape kind.
func (nzbget) Probe(ctx context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	switch kind {
	case shape.KindHealth:
		return probeNZBHealth(ctx, t)
	case shape.KindQueue:
		return probeNZBQueue(ctx, t)
	default:
		return nil, adapter.ErrUnknownKind
	}
}

// nzbStatus is the relevant subset of the status method's result.
type nzbStatus struct {
	DownloadRateLo  uint32
	DownloadRateHi  uint32
	DownloadPaused  bool
	ServerStandBy   bool
	QuotaReached    bool
	FreeDiskSpaceLo uint32
	FreeDiskSpaceHi uint32
}

// speed is the download speed now, in bytes/s: none while NZBGet stands by.
func (s nzbStatus) speed() int64 {
	if s.ServerStandBy {
		return 0
	}
	return join(s.DownloadRateHi, s.DownloadRateLo)
}

// nzbGroup is the relevant subset of one listgroups item.
type nzbGroup struct {
	NZBID           int64
	NZBName         string
	Status          string
	FileSizeLo      uint32
	FileSizeHi      uint32
	RemainingSizeLo uint32
	RemainingSizeHi uint32
	PausedSizeLo    uint32
	PausedSizeHi    uint32
	Health          int
	CriticalHealth  int
}

// nzbID is an item's id as text, "" for none.
func nzbID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

// nzbStatusWords names NZBGet's queue states that do not read well as they are, lower case with spaces.
var nzbStatusWords = map[string]string{
	"PP_QUEUED":              "waiting",
	"PP_FINISHED":            "finished",
	"QS_QUEUED":              "waiting",
	"QS_EXECUTING":           "executing script",
	"POST_UNPACK_RENAMING":   "renaming",
	"POST_DOWNLOAD_RENAMING": "renaming",
}

func nzbStatusWord(s string) string {
	if w, ok := nzbStatusWords[s]; ok {
		return w
	}
	return strings.ToLower(strings.ReplaceAll(s, "_", " "))
}

// probeNZBQueue reports the queue with its speed. NZBGet does not say when an item will be done; while it
// downloads, an item's ETA is how long the bytes still to download up to and including it take at the speed now.
func probeNZBQueue(ctx context.Context, t adapter.Target) (any, error) {
	var st nzbStatus
	if err := nzbCallInto(ctx, t, nzbMethodStatus, &st); err != nil {
		return nil, err
	}
	q := shape.Queue{Items: []shape.QueueItem{}, Speed: st.speed(), Paused: st.DownloadPaused}
	var ahead int64 // bytes to download before the current item is done
	err := nzbCall(ctx, t, nzbMethodGroups, []any{0}, nzbQueueBytes, func(d *json.Decoder) error {
		var err error
		q.Total, err = adapter.CountArray(d, shape.MaxQueueItems, func(g nzbGroup) {
			size, left := join(g.FileSizeHi, g.FileSizeLo), join(g.RemainingSizeHi, g.RemainingSizeLo)
			item := shape.QueueItem{
				Title:     shape.Clip(g.NZBName),
				ID:        nzbID(g.NZBID),
				Status:    shape.Clip(nzbStatusWord(g.Status)),
				Progress:  progress(float64(size), float64(left)),
				SizeBytes: size,
				LeftBytes: left,
			}
			if g.Health < g.CriticalHealth {
				item.Problem = "too many articles are missing to repair it"
			}
			if active := max(0, left-join(g.PausedSizeHi, g.PausedSizeLo)); active > 0 && g.Status != "PAUSED" {
				ahead += min(active, maxBytes-ahead) // saturates at maxBytes
				if q.Speed > 0 && !q.Paused {
					item.ETA = clock(ahead / q.Speed)
				}
			}
			q.Items = append(q.Items, item)
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return q, nil
}

// nzbHistoryItem is the relevant subset of one history item.
type nzbHistoryItem struct {
	Name        string
	Status      string
	HistoryTime int64
}

// nzbFailureReasons explains NZBGet's failure states ("FAILURE/<reason>").
var nzbFailureReasons = map[string]string{
	"PAR":    "Repair failed",
	"UNPACK": "Unpacking failed",
	"MOVE":   "Moving the files failed",
	"HEALTH": "Too many articles are missing",
	"BAD":    "Marked bad",
	"SCAN":   "The NZB file could not be read",
	"FETCH":  "The NZB file could not be fetched",
}

// readFailures reads the history, newest first, until an item older than the window, counting the failures.
func readFailures(d *json.Decoder, f *failures) error {
	tok, err := d.Token()
	if err != nil || tok == nil {
		return err
	}
	if tok != json.Delim('[') {
		return errors.New("not a JSON array")
	}
	for n := 0; d.More(); n++ {
		if n == maxHistoryRead {
			f.atLeast = true
			return errEnough
		}
		var h nzbHistoryItem
		if err := d.Decode(&h); err != nil {
			return err
		}
		if h.HistoryTime < f.cutoff {
			return errEnough
		}
		if state, failed := strings.CutPrefix(h.Status, "FAILURE/"); failed {
			reason := cmp.Or(nzbFailureReasons[state], strings.ToLower(strings.ReplaceAll(state, "_", " ")))
			f.add(h.HistoryTime, shape.Clip(h.Name), shape.Clip(reason))
		}
	}
	return errEnough
}

// probeNZBHealth reports the version and latency, a paused downloader, a reached quota, low disk space and the
// downloads that failed in the last day.
func probeNZBHealth(ctx context.Context, t adapter.Target) (any, error) {
	version, latency, err := fetchNZBVersion(ctx, t)
	if err != nil {
		return nil, err
	}
	var st nzbStatus
	if err := nzbCallInto(ctx, t, nzbMethodStatus, &st); err != nil {
		return nil, err
	}
	failed := newFailures(time.Now())
	if err := nzbCall(ctx, t, nzbMethodHistory, []any{false}, nzbAnswerBytes, func(d *json.Decoder) error {
		return readFailures(d, &failed)
	}); err != nil {
		return nil, err
	}

	h := shape.Health{Version: shape.Clip(version), LatencyMS: latency.Milliseconds(), Warnings: []shape.Warning{}}
	if st.DownloadPaused {
		h.Warnings = append(h.Warnings, pausedWarning)
	}
	if st.QuotaReached {
		h.Warnings = append(h.Warnings, shape.Warning{Level: shape.LevelWarning, ID: "quota",
			Message: "the download quota is reached"})
	}
	if w, ok := diskWarning(join(st.FreeDiskSpaceHi, st.FreeDiskSpaceLo)); ok {
		h.Warnings = append(h.Warnings, w)
	}
	if w, ok := failed.warning(); ok {
		h.Warnings = append(h.Warnings, w)
	}
	return h, nil
}

// MatchesImage recognises NZBGet's own image and the LinuxServer.io build.
func (nzbget) MatchesImage(image string) bool {
	switch adapter.ImageRepo(image) {
	case "nzbgetcom/nzbget", "ghcr.io/nzbgetcom/nzbget", "linuxserver/nzbget", "lscr.io/linuxserver/nzbget",
		"ghcr.io/linuxserver/nzbget":
		return true
	}
	return false
}

// ReadConfig reads NZBGet's nzbget.conf from its config folder: ControlUsername and ControlPassword as the
// credentials, and ControlPort, where NZBGet answers plain HTTP whether or not it also serves HTTPS. The host is
// left for the user (or a matching container) to complete.
func (nzbget) ReadConfig(name string, dir fs.FS) (adapter.Candidate, bool, error) {
	const file = "nzbget.conf"
	body, err := adapter.ReadConfigFile(dir, file)
	if errors.Is(err, fs.ErrNotExist) {
		return adapter.Candidate{}, false, nil
	}
	if err != nil {
		return adapter.Candidate{}, false, err
	}
	conf := configValues(body, "", "ControlUsername", "ControlPassword", "ControlPort")
	u := &url.URL{Scheme: "http", Path: "/"}
	if port := configPort(conf["ControlPort"]); port != 0 {
		u.Host = ":" + strconv.Itoa(port)
	}
	c := adapter.Candidate{Type: "nzbget", Name: "NZBGet", BaseURL: u}
	// Without a password NZBGet asks for no credentials. A user name with a colon cannot be sent as user:password.
	if user, pass := conf["ControlUsername"], conf["ControlPassword"]; pass != "" && user != "" &&
		!strings.Contains(user, ":") {
		c.APIKey, c.KeySource = user+":"+pass, file+" in "+name
	}
	return c, true, nil
}
