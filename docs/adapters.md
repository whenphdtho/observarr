# Writing an adapter

Observarr talks to every app through one interface. This is the contract adapters implement, how
to add a new *arr family member or an entirely new app, and the rules that keep one adapter from
ever being able to affect another.

## The modularity rule

```
adapter definitions ──► adapter.Registry ──► poller ──► state hub ──► SSE ──► UI
 (internal/adapters/*)   (one interface)                (shapes only)        widget per shape (web/src/lib/shapes)
```

- **The backend library is the adapter catalog.** Every app type implements `adapter.Adapter`
  (`internal/adapter`). `internal/adapters/catalog` registers all of them explicitly — nothing
  outside `internal/adapters/...` is allowed to name an app type. The poller, the state hub, the
  API and the UI never see "sonarr" or "radarr"; they see a `Type` string and a set of shape
  kinds.
- **The data contract is shapes.** Adapters fill shared shapes (`internal/shape`: `Health`,
  `Queue`, more in later waves). Whatever an app actually calls its fields, the adapter maps them
  onto the shape before anything else in Observarr sees them.
- **The UI library is a widget per shape.** `web/src/lib/shapes/` holds one component per shape
  kind. A service card renders the widget for each kind its adapter declares. Adding an app never
  touches the UI; adding a shape adds one widget.

If a change to add or modify an app needs to touch anything under `web/src` by name, or anything
outside `internal/adapters/<your-package>` and `internal/adapters/catalog`, something is wrong.

## The `Adapter` contract

From `internal/adapter/adapter.go`:

```go
// Info describes an adapter for the UI's "Add apps" page.
type Info struct {
	Type        string       `json:"type"`        // stable id, e.g. "sonarr"; stored with each service
	Name        string       `json:"name"`        // display name, e.g. "Sonarr"
	DefaultPort int          `json:"defaultPort"` // suggested in the form
	NeedsAPIKey bool         `json:"needsApiKey"`
	Kinds       []shape.Kind `json:"kinds"` // shapes this adapter can fill; the UI shows only these
	Icon        string       `json:"icon"`  // icon name hint, e.g. "tv"; the UI falls back to a generic icon
	// KeyLabel names the secret field when it holds something other than an API key, e.g. "Credentials". With
	// NeedsAPIKey false, a non-empty KeyLabel makes the field optional rather than hidden.
	KeyLabel string `json:"keyLabel,omitempty"`
	KeyHint  string `json:"keyHint,omitempty"` // shown with the field, e.g. the form the secret takes
}

// Target is one configured service as the adapter sees it.
type Target struct {
	BaseURL *url.URL     // scheme + host (+ optional path prefix for reverse-proxied apps)
	APIKey  string       // decrypted; never log it
	HTTP    *http.Client // shared client; adapters must not create their own
}

// Identity is what Detect confirmed.
type Identity struct {
	Version string `json:"version"`
}

// Adapter is one app type.
type Adapter interface {
	Info() Info
	// Detect confirms that t is this app (by its real status endpoint) and returns its version.
	// It returns ErrWrongApp if something else answers, ErrUnauthorized for a rejected key.
	Detect(ctx context.Context, t Target) (Identity, error)
	// Probe fills one declared shape kind. It must respect ctx's deadline.
	Probe(ctx context.Context, t Target, kind shape.Kind) (any, error)
}

var (
	ErrWrongApp     = errors.New("a different app answers at this address")
	ErrUnauthorized = errors.New("the app rejected the API key")
	ErrUnsupported  = errors.New("this app version is not supported")
	ErrUnknownKind  = errors.New("shape not supported by this adapter")
	ErrNotJSON      = errors.New("the response is not JSON")
	ErrNotFound     = errors.New("the app has no such endpoint")
)

// WrongApp returns the error for another app answering where this one was expected: one wrapping
// ErrWrongApp that reads "<app> answers at this address", app as that app names itself. Without a
// usable name (empty, unprintable) it is ErrWrongApp itself.
func WrongApp(app string) error
```

`Detect` is called whenever a service is added or its URL/key changes. It must actually confirm
the app's identity (not just "something answered on this port") and return `adapter.WrongApp(name)`
for anything else, including a login page, a different app, or an empty/garbled response — `name`
is whatever field the real app would have answered its own name in (e.g. an `appName` field), so
the error can say which app is actually there when it's known; pass `""` (falling back to the
plain `ErrWrongApp`) when nothing of the sort is available. `Probe` fills exactly one shape kind
per call and must never block past `ctx`'s deadline.

Adapters GET JSON through the shared helper (`internal/adapter/httpjson.go`):

```go
// GetJSON GETs base+path with the API key header (if key != "") and decodes at most 4 MiB of JSON into v.
func GetJSON(ctx context.Context, t Target, path string, keyHeader string, v any) error
```

It joins `t.BaseURL`'s path prefix with `path`, sets `Accept: application/json`, maps
401/403 → `ErrUnauthorized` and 404 → `ErrNotFound`, bounds the body to 4 MiB, wraps a body that
fails to decode as JSON in `ErrNotJSON` (dropping the body and the decoder's own message, since
either can echo back response fragments), and never includes response bytes or the API key in any
error it returns.

For a response whose interesting part is one long array (a queue, a list of warnings), use
`adapter.StreamJSON` instead: it does the same request and sets up the same 4 MiB-bounded decoder,
but hands you the `*json.Decoder` directly instead of decoding the whole body into a value, so you
can stop reading once you have enough. Pair it with `adapter.DecodeArray(d, max, each)`, which reads
a JSON array element by element and stops — without reading the rest — once `max` elements have been
handed to `each`; use it with `shape.MaxQueueItems` or `shape.MaxWarnings` so a list the app reports
as huge is capped while it's still being decoded, not after.

## Adding a family member to an existing adapter

The *arr family (Sonarr, Radarr, Lidarr, Prowlarr) is one adapter (`internal/adapters/arr`)
configured per app by `arr.Definition`:

```go
// Definition configures the shared *arr adapter for one app of the family.
type Definition struct {
	Type, Name  string
	AppName     string // value of appName in /system/status, e.g. "Sonarr"
	APIVersion  string // "v3" or "v1"
	DefaultPort int
	MinMajor    int    // oldest supported major version
	HasQueue    bool
	QueueQuery  string // extra query parameters for /queue, e.g. "&includeUnknownSeriesItems=true"
	Icon        string // icon name hint for the UI, e.g. "tv"
}

var Definitions = []Definition{ /* one entry per app */ }
```

If a new app speaks the same *arr API shape, adding it is:

1. One more entry in `arr.Definitions` (type id, display name, the app's own `appName`, API
   version, default port, minimum supported major version, whether it has a queue, any extra
   query parameters its `/queue` endpoint needs, and an icon name hint).
2. Fixtures under `internal/adapters/arr/testdata/` for its `/system/status` response (and
   `/health`, `/queue` if it differs meaningfully from the existing ones).
3. A test asserting `Detect` succeeds against its fixture and that another family member's
   adapter rejects it with `ErrWrongApp`.

Nothing else changes: `catalog.Register` already loops over every entry in `arr.Definitions`, and
the UI already renders whatever `Kinds` the new `Info()` reports.

## Adding a new adapter package

For an app with its own API shape:

1. Create `internal/adapters/<name>/<name>.go` implementing `adapter.Adapter`: `Info`, `Detect`,
   `Probe`. Use `adapter.GetJSON` for every call — never construct or use any other HTTP client
   (`Target.HTTP` is the one shared client; see "Rules" below for why).
2. Add fixtures under `internal/adapters/<name>/testdata/`, hand-written from the app's public API
   docs (never captured from a real instance), with a short `README.md` saying so.
3. Register it in `internal/adapters/catalog/catalog.go`:

   ```go
   func Register(r *adapter.Registry) {
   	for _, d := range arr.Definitions {
   		r.Register(arr.New(d))
   	}
   	r.Register(yourapp.New())
   }
   ```

4. Write the required tests (httptest, serving your fixtures):
   - `Detect` succeeds against a real fixture for this app.
   - `Detect` returns `ErrWrongApp` against a fixture from a different app (or an HTML page).
   - `Detect` returns `ErrUnauthorized` on a 401/403.
   - `Detect` returns `ErrUnsupported` for a version below what you support, if you enforce one.
   - `Probe` succeeds for every kind in `Info().Kinds`, mapping fields onto the shape correctly.
   - `Probe` returns `ErrUnknownKind` for a kind not in `Info().Kinds`.

## Rules every adapter follows

- **No own HTTP client.** Always use `t.HTTP` (via `adapter.GetJSON`), never
  `http.DefaultClient` or a constructed one. The shared client enforces timeouts, a small
  per-host connection pool, and — critically — refuses to dial link-local addresses (`169.254.0.0/16`,
  `fe80::/10`) and the cloud metadata addresses outside those ranges (`fd00:ec2::254` on AWS,
  `100.100.100.200` on Alibaba Cloud), and refuses to follow a redirect to a different host, so an
  API key can never leak to somewhere it wasn't meant for. An adapter that builds its own client
  bypasses all of that.
- **Respect the deadline.** Every `Detect`/`Probe` call receives a `ctx` already bound to a
  timeout; pass it straight through to every request. A probe that overruns leaves a reading
  error, not a hung poller loop.
- **Never log the API key**, and never let it appear in an error message, including a wrapped
  network error. `adapter.GetJSON`'s errors already satisfy this; if you build your own error
  text, never interpolate `Target.APIKey` or a raw response body into it.
- **Bound free text.** Apply `shape.Clip` to every string the app itself supplies before putting
  it in a shape (a queue item's title, a warning's message, a version string) — one hostile or
  buggy app must never be able to inflate a reading without limit. `shape.Clip` truncates at
  `shape.MaxText` bytes without splitting a UTF-8 rune.
- **Empty slices, not nil.** `shape.Health.Warnings` and `shape.Queue.Items` must be set to
  `[]T{}` when there is nothing to report, never left `nil` — nil marshals as JSON `null`. The UI
  tolerates `null` either way, but the rule stands: it is what the tests check, and an adapter
  that gets it right never needs the UI's tolerance.
- **Cap list sizes.** `shape.Queue.Items` is capped at `shape.MaxQueueItems` and
  `shape.Health.Warnings` at `shape.MaxWarnings` (both 50); `Queue.Total` still reports the app's
  real count even when `Items` is capped.

## Example: the CrowdSec adapter

`internal/adapters/crowdsec` is a small adapter with its own API shape, and the one that fills the
`security` shape (`shape.Security`: counts by type, origin and scope, the top local scenarios, and at most
`shape.MaxSecurityDecisions` recent local decisions).

- **Type** `crowdsec`, **icon** `shield`, default port 8080, kinds `health` and `security`. The key is a
  CrowdSec *bouncer* key, sent as `X-Api-Key`; it can only read decisions.
- **Detect** asks the LAPI about one address (`GET /v1/decisions?ip=127.0.0.1`). Any decision list — `null`,
  `[]` or entries — confirms it; 401/403 is `ErrUnauthorized`; a 404 or anything that is not a JSON array is
  `ErrWrongApp`. The LAPI does not tell a bouncer its version, so `Identity.Version` stays empty (the health
  widget then shows no version chip). The same quick call is the health probe.
- **Probe(security)** reads `GET /v1/decisions`, every active decision. A community blocklist can hold tens of
  thousands of entries, so the adapter uses `adapter.StreamJSONLimit` with a 48 MiB limit instead of the usual
  4 MiB, and `adapter.DecodeArray` hands it one decision at a time: each is counted, only the newest local ones
  (origins other than `CAPI` and `lists`) are kept, every breakdown tracks at most 256 distinct names, and
  every kept text is clipped. If the answer is still over the limit, the counts so far are reported with
  `Capped` set rather than failing the probe.
- The security kind is polled once a minute (`cmd/observarr`: `poller.Config.Intervals`), not every 10 s.
- **Out of scope:** alerts (`/v1/alerts`) need machine credentials, which can also write to the LAPI, so the
  adapter never asks for them; nothing is ever added to or removed from CrowdSec.

`adapter.StreamJSONLimit(ctx, t, path, keyHeader, limit, read)` is `StreamJSON` with a body limit of your own
(at most `adapter.MaxStreamBytes`, 64 MiB). Use it only when `read` keeps a bounded amount of what it decodes:
the limit bounds what is read from the network, not what your callback holds on to.

## Discovery (optional)

Discovery (`internal/discovery`) *suggests* services — from a user-started network scan, from apps
already registered, and from mounted app config folders — but never adds or polls a service, or
sends a found key anywhere, without the user clicking Add on a specific suggestion. An adapter opts into one or more sources by
implementing the matching optional interface from `internal/adapter/discovery.go`; the discovery
package finds them with a type assertion, so an adapter with no unauthenticated signal (or nothing
to link, or no config file worth reading) simply implements none of them. Nothing in
`internal/discovery` names an app type.

The Add apps page lists the apps on the server as soon as it opens (`GET /api/discovery/local`,
`Engine.Local`): it only uses `ImageMatcher` on the sampler's container list and `ConfigReader` on
the config folders — never `Fingerprint`, `Detect` or any other request to an app. An app is first
contacted when the user clicks "Test & add" on its row, which is `POST /api/services` with the
suggestion's id (Detect runs there, as for any add).

```go
// Fingerprinter recognises the app without credentials (used by the network scan).
type Fingerprinter interface {
	Fingerprint(ctx context.Context, t Target) (ok bool, version string, err error)
}

// Linker lists other apps this app is connected to (e.g. Prowlarr's applications).
type Linker interface {
	LinkedApps(ctx context.Context, t Target) ([]Candidate, error)
}

// ConfigReader reads this app's settings from its config folder (mounted read-only into
// Observarr).
type ConfigReader interface {
	ReadConfig(name string, dir fs.FS) (c Candidate, ok bool, err error)
}
```

- **Fingerprint is unauthenticated only.** It runs during a network scan of addresses the user
  typed in, before any key is known to belong to that address — it must never send one, not even a
  key saved for a different service. Use only unauthenticated requests (no `Target.APIKey`, no
  key header) and read at most a bounded amount of the response (64 KiB is what the *arr adapter
  uses); the version it returns, if any, must come from a field the app reveals without a key, else
  return "". A redirect that the shared client refuses for crossing hosts
  (`adapter.ErrCrossHostRedirect`, from `adapter.NewHTTPClient`'s own cross-host-redirect refusal)
  usually means a reverse proxy or SSO gateway sits in front of the real app; report it as
  `ok=false, err=nil`, not an error — it says nothing about whether the app itself is there. Return
  an error only when the request itself failed in a way that says nothing about what's listening
  (couldn't connect, timed out, context cancelled); any response you did get — wrong status,
  unparseable body, no matching signal — is simply "not this app" (`ok=false, err=nil`), since a
  scan inevitably reaches addresses that answer with something else entirely.
- **LinkedApps can suggest type, name and URL with no key at all**, and that is a normal, expected
  outcome, not a bug to work around. Prowlarr's own `/api/v1/applications` masks every `apiKey`
  field to the literal string `"********"` before it is ever serialised, so a linked candidate
  from Prowlarr never carries a usable key; treat a masked or empty key field as "no key found" and
  leave `Candidate.APIKey` and `KeySource` both empty in that case, same as if the field were
  absent. The user types the key themselves, or adds the service and supplies it from a
  config-folder find of the same app later.
- **Keys only ever flow into `Candidate.APIKey`.** `Fingerprint` must never find or return one (see
  above); only `LinkedApps` (when an app's own API hands one back unmasked, which is not the norm)
  and `ConfigReader` (reading a key straight out of a mounted config file) legitimately set it.
  Whatever sets it must also set `KeySource` to something the user can act on — like
  `config.xml in <folder>` for a config-folder find, or `Prowlarr: <name>` for a linked
  application — since the API never re-exposes the key itself, only whether one was found and
  where.
- **ReadConfig gets a `name` hint and a read-only `fs.FS` rooted at one mounted config folder** — a
  subdirectory of `OBSERVARR_APP_CONFIGS`, named by whatever the operator called the bind mount
  (e.g. `sonarr` from `/srv/sonarr/config:/appconfig/sonarr:ro`). Use `name` as a fallback hint only
  for apps whose own config carries no app-identifying field; prefer a field in the config itself
  when there is one. `fs.Lstat` the file you care about *before* opening it and require
  `Mode().IsRegular()` — a config folder is not guaranteed to hold only regular files: `Open` on
  a FIFO can block indefinitely waiting for a writer that will never come, which would hang the
  whole discovery run, and a symbolic link could lead to any file in the container (`Lstat`, unlike
  `Stat`, does not follow it). Cap how much you read (the *arr adapter rejects anything over 1 MiB) and
  never include file contents in an error. `ok=false, err=nil` means "not my app's config"; a
  genuine read/parse problem is `err != nil`.
- **Tests to write:** `Fingerprint` — recognised, each kind of non-match (wrong/missing signal,
  non-200, unparseable body), the cross-host-redirect case (`ok=false, err=nil`), a transport
  failure (`err != nil`); `LinkedApps` — a masked key leaves `APIKey`/`KeySource` empty, an
  genuinely unmasked key is still used, an unknown type or a non-http(s)/hostless URL is skipped;
  `ConfigReader` — recognised via the config's own identifying field and via the `name` fallback,
  another app's config (`ok=false`), an oversized or malformed file (`err != nil`, no file content
  in the message), and — where the OS allows making one — a FIFO in place of the config file
  (`ok=false` with an error, and the test itself must not hang).

## Adding a new shape

A shape is the data contract between adapters and the UI; adding one touches exactly three
places:

1. **`internal/shape`** — add the `Kind` constant and the Go type (with JSON tags, following the
   existing `Health`/`Queue` style: empty slices, `shape.Clip` on free text, a doc comment on
   every exported identifier).
2. **`web/src/lib/shapes/<Name>.svelte`** — a widget that receives `{ reading }` (a `shape.Reading`
   — `{ data?, error?, at }`) and renders it. It must not know or care which app produced the
   reading.
3. **`web/src/lib/shapes/index.js`** — one more line in the `widgets` map from the shape's kind
   string to the new component. This is the only place that maps a kind to a component.

Any adapter that wants to fill the new shape adds its `Kind` to `Info().Kinds` and a case in its
`Probe` switch — nothing in `internal/state`, `internal/api`, or the UI shell needs to change.

## rclone: mounts through a POST-only API

`internal/adapters/rclone` reads rclone's remote-control API (`rclone rcd`, or a mount started with `--rc`). It
fills `health` and `mounts`; deployment is in [deploy.md](deploy.md#connect-rclone).

- **Read-only calls that are POSTs.** rclone's remote control takes every call as a POST with JSON parameters.
  `adapter.PostJSON(ctx, t, path, body, authorize, read)` sends one through the shared client with the same
  4 MiB answer bound, status mapping and error hygiene as `StreamJSON`; the request body is limited to 64 KiB.
  Use it only for calls that change nothing. The rclone adapter keeps an allow-list of exactly the calls it
  makes — `core/version`, `rc/noopauth`, `core/stats`, `mount/listmounts`, `vfs/list`, `vfs/stats` — and refuses
  any other path before a request is built; its tests fail on any request outside that list.
- **Credentials other than an API key.** `Info.KeyLabel` names the secret field ("Credentials") and
  `Info.KeyHint` explains its form; with `NeedsAPIKey` false and a `KeyLabel`, the form shows the field as
  optional. rclone's secret is `user:password` for HTTP basic auth (`--rc-user`/`--rc-pass`), stored sealed
  like any key. `Detect` checks it with `rc/noopauth`, since `core/version` needs no authentication.
- **Field names** are checked against rclone's source and recorded in the package comment: `vfs/stats` returns
  the VFS options under `opt` with Go field names (`CacheMode` as its index 0–3, `CacheMaxSize` in bytes, -1 for
  no limit) and a `diskCache` object only when the cache mode is above `off`.
- **Mount points** come from `mount/listmounts`, which only lists mounts made through the remote control; every
  VFS from `vfs/list` is reported, as `mounted` with its mount point when listed and `active` otherwise.

The `mounts` shape (`shape.Mounts`) holds at most `shape.MaxMounts` (20) mounts — each with remote, mount
point, state (`mounted`, `active`, or `unmounted` for apps that keep configured mounts), cache mode, cache used
and limit, cached files, uploads in progress and queued, errored files and out-of-space — plus the transfer
speed, totals and at most `shape.MaxTransfers` (20) transfers in progress; `Total` and `ActiveTransfers` keep
the real counts. The Storage view and the `Mounts` widget render it.

## SABnzbd and NZBGet: two downloaders in one package

`internal/adapters/usenet` holds two adapters, `sabnzbd` and `nzbget`, that report the same things the same way:
`health` and `queue`. The field names of both APIs are checked against the apps' source and recorded in each
file's comment.

- **SABnzbd** (`GET /api?mode=…&output=json&apikey=…`) takes its key as a query parameter, and net/http's
  errors repeat the request URL. Every call goes through `sabGet`, which takes the query out of any `*url.Error`
  before the error goes anywhere; a test fails if the key shows up in an error. The calls are fixed values
  (`mode=version`, `queue`, `history&failed_only=1`, `warnings`); none carries a `name`, which would turn a mode
  into an action.
- **NZBGet** speaks JSON-RPC over POST at `/jsonrpc`, through `adapter.PostJSON` with an allow-list of the
  methods it calls (`version`, `status`, `listgroups`, `history`). Its secret is `user:password` for basic auth,
  read by `adapter.BasicAuth`, the same as rclone's. Its history has no limit, so the adapter reads it as a
  stream and stops at the first item older than a day (or after 200 items).
- **The queue shape** carries two optional fields for an app that downloads the whole queue itself: `speed`
  (bytes/s) and `paused`. Both are left out when zero or false, so an \*arr queue looks as before; the `Queue`
  widget shows them only when set, and the Overview's Downloads figure adds up every reported speed.
- **Health** warns of a paused downloader, less than 1 GiB free for downloads, the downloads that failed in the
  last day (with the newest one's name and reason) and, for SABnzbd, the warnings it logged in the last day.
- **Discovery:** both recognise their Docker images and read their config folder (`sabnzbd.ini`: `api_key`,
  `port`, `url_base`; `nzbget.conf`: `ControlUsername`, `ControlPassword`, `ControlPort`) through
  `adapter.ReadConfigFile`, and both answer a network scan without a key: SABnzbd's `mode=version` needs none,
  and NZBGet names itself in the `Server` header of every answer, a refusal included.

## Jellyfin: streams

`internal/adapters/jellyfin` reads Jellyfin's REST API with an API key. It fills `health` and `streams`;
deployment is in [deploy.md](deploy.md#connect-jellyfin).

- **Calls.** `GET /System/Info/Public` (no key) recognises Jellyfin by its `ProductName` and is the network
  scan's fingerprint; `GET /System/Info` checks the key and gives the version and the server's own state (a
  pending restart or a shutdown becomes a warning); `GET /Sessions?activeWithinSeconds=960` lists the sessions.
  Field names are checked against Jellyfin's source and recorded in the package comment.
- **The key** goes in `Authorization: MediaBrowser Token="<key>"`, the form Jellyfin keeps; the `X-Emby-Token`
  header is deprecated and off by default from Jellyfin 12. A key with anything but letters, digits and dashes is
  refused before a request is built, so it cannot break out of the quotes.
- **Play method.** Jellyfin reports a remux, and a stream whose audio alone is converted, as `Transcode`; with
  `TranscodingInfo.IsVideoDirect` that is a direct stream, as Jellyfin's own dashboard shows it. Transcode
  reasons (a flags enum the server writes as an array of names) become plain words, e.g.
  `ContainerBitrateExceedsLimit` is "bitrate too high".
- **Privacy.** A session carries the client's address (`RemoteEndPoint`); the adapter has no field for it, so it
  is never read, kept or sent to the browser. User names are kept.
- **Emby** is not covered: its public system info has no product name to recognise it by, and it authenticates
  differently. It would need an adapter of its own, tested against a real server.

The `streams` shape (`shape.Streams`) counts every session playing something — in all, paused, transcoding —
and sums the bitrate, and lists at most `shape.MaxStreams` (50), sorted by user and title. Each stream has the
user, client and device, a title ("Series S01E02 · Episode", "Film (2008)"), paused or not, progress, the play
method (`direct`, `directstream`, `transcode`), at most `shape.MaxStreamReasons` (8) reasons, the codecs of the
file and what the server converts them to, and the bitrate in bits/s. The poller reads it every 10 s while
someone watches, like a queue. The `Streams` widget, the Overview's "Now playing" section, the service card and
the Network view render it. A transcode is not a warning: the adapter has nothing that says it is a problem.
