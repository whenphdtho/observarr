// Package rclone implements the Observarr adapter for rclone's remote-control API (an "rclone rcd", or an "rclone
// mount" started with --rc). It reports rclone's mounts, their VFS disk caches and the transfers in progress.
//
// rclone's remote control takes every call as a POST, reads included. This adapter only ever makes the read-only
// calls in allowedPaths; nothing else can be sent (see call). The field names below were checked against rclone's
// own source (github.com/rclone/rclone, master as of November 2026):
//
//   - core/version (fs/rc/internal.go): "version" ("v1.71.2"), "decomposed" ([1,71,2]), "goVersion" ("go1.25.0"),
//     "os", "arch", "isGit", "isBeta". Needs no authentication.
//   - rc/noopauth (fs/rc/internal.go): echoes its parameters back and needs authentication; Detect uses it to check
//     the credentials, since core/version does not.
//   - core/stats (fs/accounting/stats.go RemoteStats): "bytes", "speed" (bytes/s), "errors", "checks",
//     "transfers" (completed), "elapsedTime" (s), "lastError", and "transferring" while transfers run: a list of
//     {"name", "size", "bytes", "percentage" (0-100), "speed", "speedAvg", "eta", "group"} (accounting.go rcStats).
//     Needs no authentication.
//   - mount/listmounts (cmd/mountlib/rc.go): {"mountPoints": [{"Fs", "MountPoint", "MountedOn"}]}. It lists only
//     mounts made through the remote control (mount/mount); a mount started on the command line ("rclone mount
//     remote: /mnt --rc") is not in it, though its VFS is. Needs authentication, and is missing (404) from a build
//     without mount support.
//   - vfs/list (vfs/rc.go): {"vfses": ["remote:path", …]}; a name used by several VFSes is listed as "name[0]",
//     "name[1]", … Each name is what vfs/stats takes as "fs". Needs no authentication.
//   - vfs/stats (vfs/rc.go, vfs.go Stats, vfscache/cache.go Stats): {"fs", "inUse", "metadataCache": {"dirs",
//     "files"}, "opt": the VFS options, and, only with a cache mode above off, "diskCache": {"bytesUsed", "files",
//     "erroredFiles", "uploadsInProgress", "uploadsQueued", "outOfSpace", "path", "pathMeta", "hashType"}}. "opt"
//     is the Go struct vfscommon.Options without JSON tags, so its keys are the Go field names: CacheMode (an enum
//     that marshals as its index 0-3, off/minimal/writes/full, since its MarshalJSON has a pointer receiver; a
//     string is accepted too) and CacheMaxSize (bytes; -1, "off", means no limit). Needs no authentication.
//
// Credentials: rclone's remote control uses HTTP basic authentication (--rc-user/--rc-pass). The service's secret
// holds them as "user:password" and is sealed like an API key. Without one, the calls that need authentication
// work only with --rc-no-auth; mount points are then not reported, everything else is.
package rclone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// Type is the adapter's stable id.
const Type = "rclone"

// The remote-control calls this adapter makes. None changes anything.
const (
	pathVersion    = "core/version"
	pathNoopAuth   = "rc/noopauth"
	pathStats      = "core/stats"
	pathListMounts = "mount/listmounts"
	pathVFSList    = "vfs/list"
	pathVFSStats   = "vfs/stats"
)

// allowedPaths is every call the adapter may make; call refuses any other.
var allowedPaths = []string{pathVersion, pathNoopAuth, pathStats, pathListMounts, pathVFSList, pathVFSStats}

// errNotAllowed is a programming error: a call outside allowedPaths.
var errNotAllowed = errors.New("rclone: call not allowed")

// ErrCredentials is returned for a secret that is not of the form "user:password".
var ErrCredentials = errors.New("credentials: enter them as user:password")

// maxName bounds a VFS name handed back to vfs/stats; a longer one is skipped.
const maxName = 1024

// maxListedMounts bounds the entries read from mount/listmounts.
const maxListedMounts = 4 * shape.MaxMounts

type rcAdapter struct{}

// New returns the rclone adapter.
func New() adapter.Adapter { return rcAdapter{} }

// Info describes the adapter for the UI's "Add service" form.
func (rcAdapter) Info() adapter.Info {
	return adapter.Info{
		Type:        Type,
		Name:        "rclone",
		DefaultPort: 5572,
		NeedsAPIKey: false,
		Kinds:       []shape.Kind{shape.KindHealth, shape.KindMounts},
		Icon:        "cloud",
		KeyLabel:    "Credentials",
		KeyHint:     "user:password of --rc-user/--rc-pass; leave blank with --rc-no-auth",
	}
}

// credentials splits key into user and password. An empty key means none.
func credentials(key string) (user, pass string, ok bool, err error) {
	if key == "" {
		return "", "", false, nil
	}
	user, pass, found := strings.Cut(key, ":")
	if !found || user == "" {
		return "", "", false, ErrCredentials
	}
	return user, pass, true, nil
}

// call POSTs params (nil for none) as JSON to one allowed remote-control path and hands the answer to read.
func call(ctx context.Context, t adapter.Target, path string, params map[string]string,
	read func(*json.Decoder) error) error {
	if !slices.Contains(allowedPaths, path) {
		return fmt.Errorf("%w: %s", errNotAllowed, path)
	}
	user, pass, withAuth, err := credentials(t.APIKey)
	if err != nil {
		return err
	}
	if params == nil {
		params = map[string]string{}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	var authorize func(*http.Request)
	if withAuth {
		authorize = func(r *http.Request) { r.SetBasicAuth(user, pass) }
	}
	return adapter.PostJSON(ctx, t, "/"+path, body, authorize, read)
}

// callInto is call decoding the whole answer into v.
func callInto(ctx context.Context, t adapter.Target, path string, params map[string]string, v any) error {
	return call(ctx, t, path, params, func(d *json.Decoder) error { return d.Decode(v) })
}

// versionResponse is the relevant subset of core/version.
type versionResponse struct {
	Version    string `json:"version"`
	GoVersion  string `json:"goVersion"`
	Decomposed []int  `json:"decomposed"`
}

func fetchVersion(ctx context.Context, t adapter.Target) (versionResponse, time.Duration, error) {
	var v versionResponse
	start := time.Now()
	err := callInto(ctx, t, pathVersion, nil, &v)
	return v, time.Since(start), err
}

// displayVersion is rclone's version without its leading "v", as the UI adds one.
func displayVersion(v string) string { return shape.Clip(strings.TrimPrefix(v, "v")) }

// Detect confirms that rclone's remote control answers at t (by core/version) and that the credentials, if any,
// are accepted (by rc/noopauth).
func (rcAdapter) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	if _, _, _, err := credentials(t.APIKey); err != nil {
		return adapter.Identity{}, err
	}
	v, _, err := fetchVersion(ctx, t)
	if err != nil {
		// Another app answers a POST to /core/version with a 404, a 405 or a page that is not JSON.
		var status adapter.StatusError
		if errors.Is(err, adapter.ErrNotJSON) || errors.Is(err, adapter.ErrNotFound) ||
			errors.As(err, &status) && status == http.StatusMethodNotAllowed {
			return adapter.Identity{}, adapter.ErrWrongApp
		}
		return adapter.Identity{}, err
	}
	if !strings.HasPrefix(v.Version, "v") || !strings.HasPrefix(v.GoVersion, "go") || len(v.Decomposed) == 0 {
		return adapter.Identity{}, adapter.ErrWrongApp
	}
	if t.APIKey != "" {
		if err := callInto(ctx, t, pathNoopAuth, nil, &json.RawMessage{}); err != nil {
			return adapter.Identity{}, err
		}
	}
	return adapter.Identity{Version: displayVersion(v.Version)}, nil
}

// Probe fills one declared shape kind.
func (rcAdapter) Probe(ctx context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	switch kind {
	case shape.KindHealth:
		return probeHealth(ctx, t)
	case shape.KindMounts:
		return probeMounts(ctx, t)
	default:
		return nil, adapter.ErrUnknownKind
	}
}

// probeHealth reports the version and latency, and warns of a cache that is out of space or holds files rclone
// could not upload.
func probeHealth(ctx context.Context, t adapter.Target) (any, error) {
	v, latency, err := fetchVersion(ctx, t)
	if err != nil {
		return nil, err
	}
	mounts, _, err := fetchMounts(ctx, t)
	if err != nil {
		return nil, err
	}
	warnings := []shape.Warning{}
	for _, m := range mounts {
		if len(warnings) >= shape.MaxWarnings {
			break
		}
		switch {
		case m.OutOfSpace:
			warnings = append(warnings, shape.Warning{Level: shape.LevelError, Source: m.Remote,
				Message: "the cache disk is out of space"})
		case m.ErroredFiles > 0:
			warnings = append(warnings, shape.Warning{Level: shape.LevelWarning, Source: m.Remote,
				Message: plural(m.ErroredFiles, "cached file") + " could not be uploaded"})
		}
	}
	return shape.Health{Version: displayVersion(v.Version), LatencyMS: latency.Milliseconds(), Warnings: warnings}, nil
}

func plural(n int64, word string) string {
	s := strconv.FormatInt(n, 10) + " " + word
	if n != 1 {
		s += "s"
	}
	return s
}

// probeMounts reports every mount with its cache, and the transfers in progress.
func probeMounts(ctx context.Context, t adapter.Target) (any, error) {
	mounts, total, err := fetchMounts(ctx, t)
	if err != nil {
		return nil, err
	}
	out := shape.Mounts{Total: total, Mounts: mounts, Transfers: []shape.Transfer{}}
	if err := call(ctx, t, pathStats, nil, func(d *json.Decoder) error { return decodeStats(d, &out) }); err != nil {
		return nil, err
	}
	return out, nil
}

// listedMount is one entry of mount/listmounts.
type listedMount struct {
	Fs         string `json:"Fs"`
	MountPoint string `json:"MountPoint"`
}

// fetchMounts combines vfs/list, vfs/stats and mount/listmounts into at most MaxMounts mounts, sorted by mount
// point and remote, and reports how many there are in all.
func fetchMounts(ctx context.Context, t adapter.Target) ([]shape.Mount, int, error) {
	var names []string
	total := 0
	err := call(ctx, t, pathVFSList, nil, func(d *json.Decoder) error {
		return decodeObject(d, map[string]func(*json.Decoder) error{
			"vfses": func(d *json.Decoder) error {
				var err error
				total, err = decodeCapped(d, shape.MaxMounts, func(name string) {
					if name != "" && len(name) <= maxName {
						names = append(names, name)
					}
				})
				return err
			},
		})
	})
	if err != nil {
		return nil, 0, err
	}

	listed, err := fetchListedMounts(ctx, t)
	if err != nil {
		return nil, 0, err
	}

	mounts := make([]shape.Mount, 0, len(names))
	for _, name := range names {
		m, err := fetchVFS(ctx, t, name)
		var status adapter.StatusError
		if errors.As(err, &status) {
			continue // the VFS went away since vfs/list
		}
		if err != nil {
			return nil, 0, err
		}
		if m.Remote == "" {
			m.Remote = shape.Clip(baseName(name))
		}
		// A mount made through the remote control is listed with its mount point; match it by remote, in order.
		base := baseName(name)
		if i := slices.IndexFunc(listed, func(l listedMount) bool { return l.Fs == base }); i >= 0 {
			m.State = shape.MountMounted
			m.MountPoint = shape.Clip(listed[i].MountPoint)
			listed = slices.Delete(listed, i, i+1)
		}
		mounts = append(mounts, m)
	}
	// Listed mounts without a VFS of their own (never seen in practice) still show, without cache figures.
	for _, l := range listed {
		if len(mounts) == shape.MaxMounts {
			break
		}
		mounts = append(mounts, shape.Mount{Remote: shape.Clip(l.Fs), MountPoint: shape.Clip(l.MountPoint),
			State: shape.MountMounted})
		total++
	}
	slices.SortFunc(mounts, func(a, b shape.Mount) int {
		if c := strings.Compare(a.MountPoint, b.MountPoint); c != 0 {
			return c
		}
		return strings.Compare(a.Remote, b.Remote)
	})
	return mounts, max(total, len(mounts)), nil
}

// fetchListedMounts reads mount/listmounts. A build without mount support (404) lists none; so does a remote
// control that needs credentials when none are configured, as the other calls still work without them.
func fetchListedMounts(ctx context.Context, t adapter.Target) ([]listedMount, error) {
	var listed []listedMount
	err := call(ctx, t, pathListMounts, nil, func(d *json.Decoder) error {
		return decodeObject(d, map[string]func(*json.Decoder) error{
			"mountPoints": func(d *json.Decoder) error {
				_, err := decodeCapped(d, maxListedMounts, func(l listedMount) { listed = append(listed, l) })
				return err
			},
		})
	})
	switch {
	case errors.Is(err, adapter.ErrNotFound):
		return nil, nil
	case errors.Is(err, adapter.ErrUnauthorized) && t.APIKey == "":
		return nil, nil
	}
	return listed, err
}

// baseName is a vfs/list name without the "[n]" rclone appends when several VFSes share one remote.
func baseName(name string) string {
	if strings.HasSuffix(name, "]") {
		if i := strings.LastIndexByte(name, '['); i > 0 {
			if _, err := strconv.Atoi(name[i+1 : len(name)-1]); err == nil {
				return name[:i]
			}
		}
	}
	return name
}

// vfsStats is the relevant subset of vfs/stats.
type vfsStats struct {
	Fs        string `json:"fs"`
	DiskCache *struct {
		BytesUsed         int64 `json:"bytesUsed"`
		Files             int64 `json:"files"`
		ErroredFiles      int64 `json:"erroredFiles"`
		UploadsInProgress int64 `json:"uploadsInProgress"`
		UploadsQueued     int64 `json:"uploadsQueued"`
		OutOfSpace        bool  `json:"outOfSpace"`
	} `json:"diskCache"`
	Opt struct {
		CacheMode    json.RawMessage `json:"CacheMode"`
		CacheMaxSize json.RawMessage `json:"CacheMaxSize"`
	} `json:"opt"`
}

// fetchVFS reads one VFS's figures. Its state is MountActive until a listed mount matches it.
func fetchVFS(ctx context.Context, t adapter.Target, name string) (shape.Mount, error) {
	var s vfsStats
	if err := callInto(ctx, t, pathVFSStats, map[string]string{"fs": name}, &s); err != nil {
		return shape.Mount{}, err
	}
	m := shape.Mount{Remote: shape.Clip(s.Fs), State: shape.MountActive, CacheMode: cacheMode(s.Opt.CacheMode)}
	if c := s.DiskCache; c != nil {
		m.Cache = true
		m.CacheUsedBytes = max(0, c.BytesUsed)
		m.CacheMaxBytes = sizeBytes(s.Opt.CacheMaxSize)
		m.CachedFiles = max(0, c.Files)
		m.ErroredFiles = max(0, c.ErroredFiles)
		m.UploadsInProgress = max(0, c.UploadsInProgress)
		m.UploadsQueued = max(0, c.UploadsQueued)
		m.OutOfSpace = c.OutOfSpace
	}
	return m, nil
}

// cacheModes are rclone's VFS cache modes, by their index (vfscommon/cachemode.go).
var cacheModes = []string{"off", "minimal", "writes", "full"}

// cacheMode reads opt.CacheMode, an index into cacheModes or the mode's name.
func cacheMode(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var i int
	if json.Unmarshal(raw, &i) == nil {
		if i >= 0 && i < len(cacheModes) {
			return cacheModes[i]
		}
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && slices.Contains(cacheModes, strings.ToLower(s)) {
		return strings.ToLower(s)
	}
	return ""
}

// sizeBytes reads opt.CacheMaxSize: bytes as a number, or rclone's size text ("10Gi", "512M", "off"), in which
// rclone's units are powers of 1024. No limit (-1, "off") and anything unreadable are 0.
func sizeBytes(raw json.RawMessage) int64 {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		if n <= 0 || n > 1<<62 {
			return 0
		}
		return int64(n)
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return 0
	}
	s = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(s), "B"), "i")
	mult := float64(1 << 10) // rclone's default unit is KiB
	if s != "" {
		switch s[len(s)-1] {
		case 'b':
			mult, s = 1, s[:len(s)-1]
		case 'K', 'k':
			mult, s = 1<<10, s[:len(s)-1]
		case 'M', 'm':
			mult, s = 1<<20, s[:len(s)-1]
		case 'G', 'g':
			mult, s = 1<<30, s[:len(s)-1]
		case 'T', 't':
			mult, s = 1<<40, s[:len(s)-1]
		case 'P', 'p':
			mult, s = 1<<50, s[:len(s)-1]
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 || v*mult > 1<<62 {
		return 0
	}
	return int64(v * mult)
}

// transferring is one entry of core/stats's transferring list.
type transferring struct {
	Name       string  `json:"name"`
	Size       int64   `json:"size"`
	Percentage float64 `json:"percentage"`
	Speed      float64 `json:"speed"`
}

// decodeStats reads core/stats into out: the speed, totals and the first MaxTransfers transfers in progress.
func decodeStats(d *json.Decoder, out *shape.Mounts) error {
	var speed float64
	var bytes, errs int64
	err := decodeObject(d, map[string]func(*json.Decoder) error{
		"speed":  func(d *json.Decoder) error { return d.Decode(&speed) },
		"bytes":  func(d *json.Decoder) error { return d.Decode(&bytes) },
		"errors": func(d *json.Decoder) error { return d.Decode(&errs) },
		"transferring": func(d *json.Decoder) error {
			n, err := decodeCapped(d, shape.MaxTransfers, func(tr transferring) {
				out.Transfers = append(out.Transfers, shape.Transfer{
					Name:      shape.Clip(tr.Name),
					Progress:  min(1, max(0, tr.Percentage/100)),
					SizeBytes: max(0, tr.Size),
					Speed:     max(0, tr.Speed),
				})
			})
			out.ActiveTransfers = n
			return err
		},
	})
	out.Speed, out.Bytes, out.Errors = max(0, speed), max(0, bytes), max(0, errs)
	return err
}

// decodeObject reads a JSON object (or null) from d, handing the value of each key in fields to its function and
// skipping every other value.
func decodeObject(d *json.Decoder, fields map[string]func(*json.Decoder) error) error {
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
		if f, ok := fields[name]; ok {
			err = f(d)
		} else {
			err = skipValue(d)
		}
		if err != nil {
			return err
		}
	}
	_, err = d.Token() // the closing brace
	return err
}

// decodeCapped reads a JSON array (or null) from d, handing its first max elements, decoded as T, to each and
// skipping the rest without keeping them. It returns how many elements there were.
func decodeCapped[T any](d *json.Decoder, max int, each func(T)) (int, error) {
	tok, err := d.Token()
	if err != nil || tok == nil {
		return 0, err
	}
	if tok != json.Delim('[') {
		return 0, errors.New("not a JSON array")
	}
	n := 0
	for ; d.More(); n++ {
		if n >= max {
			if err := skipValue(d); err != nil {
				return n, err
			}
			continue
		}
		var v T
		if err := d.Decode(&v); err != nil {
			return n, err
		}
		each(v)
	}
	_, err = d.Token() // the closing bracket
	return n, err
}

// skipValue reads one JSON value from d token by token, keeping nothing.
func skipValue(d *json.Decoder) error {
	depth := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
		if depth == 0 {
			return nil
		}
	}
}
