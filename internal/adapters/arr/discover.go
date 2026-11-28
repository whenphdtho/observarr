package arr

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// maxFingerprintBytes bounds how much of an unauthenticated response Fingerprint will read: the
// /ping body is a few bytes of JSON and the index page is a small static shell, so 64 KiB is
// generous while still refusing to read an unbounded amount from whatever happens to answer at a
// scanned address.
const maxFingerprintBytes = 64 << 10

// pingResponse is GET /ping's body. Verified against the Servarr source on GitHub (2026-11-22,
// Sonarr/Sonarr, Radarr/Radarr, Lidarr/Lidarr and Prowlarr/Prowlarr @develop, all forked from the
// same NzbDrone.Host lineage and each carrying a Ping controller under <App>.Http/Ping):
//   - PingController.GetStatus is [AllowAnonymous] and [HttpGet("/ping")] — no key required.
//   - It replies 200 with {"Status":"OK"} on success or 500 with {"Status":"Error"} if the
//     app's own config-repository check throws; ASP.NET Core's JSON options set
//     PropertyNamingPolicy = JsonNamingPolicy.CamelCase (NzbDrone.Common/Serializer/
//     System.Text.Json/STJson.cs), so the field is actually serialised "status" on the wire.
type pingResponse struct {
	Status string `json:"status"`
}

// Fingerprint recognises this family member without a key, by two unauthenticated signals that
// must both hold:
//
//  1. GET /ping answers JSON {"status":"OK"} (see pingResponse's comment for how this was
//     verified). This alone only proves "a Servarr-family app", since every member serves the
//     same /ping.
//  2. GET / carries the app's name as a literal page title. Two cases, both verified against the
//     same four repositories (2026-11-22, @develop):
//     - No auth required (rare): the single-page app's static shell is served directly, titled
//     e.g. "<title>Sonarr</title>" (root index.html for Sonarr/Radarr's Vite build,
//     frontend/src/index.ejs for Lidarr and Prowlarr's webpack build).
//     - Forms auth (the default since v4): StaticResourceController's Index action is
//     [Authorize(Policy="UI")] (Sonarr.Http/Frontend/StaticResourceController.cs and the
//     Radarr/Lidarr/Prowlarr equivalents), so an unauthenticated request is redirected to
//     /login, whose own static page (frontend/src/login.html, [AllowAnonymous]) is titled
//     e.g. "<title>Login - Sonarr</title>". The shared HTTP client follows a same-host
//     redirect automatically, so this just shows up as the final response body.
//
// A redirect refused because it leads to a different host (the shared client's own
// cross-host-redirect refusal, see adapter.NewHTTPClient) is reported as ok=false, not an error:
// it usually means a reverse proxy or SSO gateway sits in front of the real app, which says
// nothing about whether the app itself is there. A 401 (e.g. HTTP Basic auth in front of the app)
// is likewise just a non-200 status, so it is already ok=false by the same rule as any other
// unexpected status.
//
// Fingerprint returns an error only when the HTTP request itself failed in a way that says
// nothing about what, if anything, is listening (could not connect, timed out, context
// cancelled); any response it did get — wrong status, non-JSON body, a mismatched or absent
// title — means simply "not this app" and is reported as ok=false, since a network scan
// inevitably reaches hosts and ports that answer something other than the app being probed.
// Version is always "": a *arr app's real version is only available once its status endpoint is
// called with a key, which Fingerprint, run before any key is known to be valid for this
// address, never does.
func (a arrAdapter) Fingerprint(ctx context.Context, t adapter.Target) (bool, string, error) {
	pingOK, err := a.pingOK(ctx, t)
	if err != nil {
		return false, "", err
	}
	if !pingOK {
		return false, "", nil
	}

	titleOK, err := a.titleMatches(ctx, t)
	if err != nil {
		return false, "", err
	}
	return titleOK, "", nil
}

func (a arrAdapter) pingOK(ctx context.Context, t adapter.Target) (bool, error) {
	status, body, err := fetchUnauthenticated(ctx, t, "/ping")
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, nil
	}
	var resp pingResponse
	if json.Unmarshal(body, &resp) != nil {
		return false, nil
	}
	return strings.EqualFold(resp.Status, "OK"), nil
}

func (a arrAdapter) titleMatches(ctx context.Context, t adapter.Target) (bool, error) {
	status, body, err := fetchUnauthenticated(ctx, t, "/")
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, nil
	}
	lower := strings.ToLower(string(body))
	app := strings.ToLower(a.def.AppName)
	return strings.Contains(lower, "<title>"+app+"</title>") ||
		strings.Contains(lower, "<title>login - "+app+"</title>"), nil
}

// fetchUnauthenticated GETs t.BaseURL+path with no API key header and no Accept header,
// regardless of t.APIKey, and returns at most maxFingerprintBytes of the body together with the
// status code: Fingerprint must never spend a key probing an address that might not even be the
// right app. Only a failure to get any HTTP response at all is returned as an error, and even
// then not always: a redirect refused for crossing hosts (adapter.ErrCrossHostRedirect) is
// reported as status=0, body=nil, err=nil — "got nothing usable", not "something is wrong" —
// since the shared client's own policy, not a network failure, is what stopped it. The body is
// never included in any error.
func fetchUnauthenticated(ctx context.Context, t adapter.Target, path string) (status int, body []byte, err error) {
	u := *t.BaseURL
	u.Path = strings.TrimSuffix(t.BaseURL.Path, "/") + path
	u.RawPath = ""
	u.RawQuery = ""

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, nil, fmt.Errorf("arr: build fingerprint request: %w", err)
	}
	resp, err := t.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, adapter.ErrCrossHostRedirect) {
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("arr: fingerprint request to %s failed: %w", u.Path, err)
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(io.LimitReader(resp.Body, maxFingerprintBytes))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("arr: read fingerprint response from %s: %w", u.Path, err)
	}
	return resp.StatusCode, body, nil
}

// linkingArrAdapter is the concrete type returned by New for a Definition with Links set (only
// Prowlarr): it adds LinkedApps to the plain arrAdapter embedded in it.
type linkingArrAdapter struct{ arrAdapter }

// applicationField is one entry of a Prowlarr application's "fields" array.
type applicationField struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// applicationItem is one entry of GET /api/v1/applications. Verified against the Prowlarr source
// on GitHub (2026-11-22, Prowlarr/Prowlarr @develop): ApplicationResource extends
// ProviderResource<T> (src/Prowlarr.Api.V1/ProviderResource.cs), which carries Name,
// Implementation (the C# provider class name, e.g. "Sonarr", "Radarr", "Lidarr") and
// Fields []Field; Field (src/Prowlarr.Http/ClientSchema/Field.cs) carries Name and Value, and
// every property goes through the same camelCase JSON naming as /ping (STJson.cs), so on the
// wire this is {"name", "implementation", "fields": [{"name", "value"}, ...]}. A linked Sonarr's
// own settings (NzbDrone.Core.Applications.Sonarr.SonarrSettings) surface in Fields as "baseUrl"
// and "apiKey" — the C# property names, camelCased.
//
// Critically, apiKey comes back masked: Prowlarr.Http/ClientSchema/SchemaBuilder.cs ToSchema
// replaces any non-empty Field whose Privacy is ApiKey or Password with the literal constant
// PRIVATE_VALUE = "********" before the resource is ever serialised, and SonarrSettings.ApiKey
// carries exactly that Privacy level. So this endpoint can never hand back a usable key — only
// confirmation that one is set. LinkedApps must never return "********" as a found key.
type applicationItem struct {
	Name           string             `json:"name"`
	Implementation string             `json:"implementation"`
	Fields         []applicationField `json:"fields"`
}

// maskedFieldValue is Prowlarr's SchemaBuilder.PRIVATE_VALUE: see applicationItem's comment.
const maskedFieldValue = "********"

// maxLinkedApps bounds how many candidates LinkedApps will ever return, so a Prowlarr instance
// with a pathological number of applications cannot inflate a discovery run without limit.
const maxLinkedApps = 100

// LinkedApps lists the apps Prowlarr is configured to sync indexers to, as discovery candidates.
// Unlike Fingerprint, this calls the app with its real (already-verified) API key: Prowlarr must
// already be a registered, keyed service for this to be reachable at all. In practice it never
// returns a usable key — Prowlarr masks every application's apiKey field whenever one is set (see
// applicationItem's comment) — but the mask is still checked explicitly here, rather than assumed,
// so a linked candidate's key and KeySource can never be anything other than "found and usable" or
// "absent": never the literal mask string.
func (a linkingArrAdapter) LinkedApps(ctx context.Context, t adapter.Target) ([]adapter.Candidate, error) {
	var items []applicationItem
	if err := adapter.GetJSON(ctx, t, "/api/v1/applications", apiKeyHeader, &items); err != nil {
		return nil, err
	}

	candidates := make([]adapter.Candidate, 0, len(items))
	for _, item := range items {
		if len(candidates) >= maxLinkedApps {
			break
		}
		def, ok := definitionByAppName(item.Implementation)
		if !ok {
			continue // not a family member we know, e.g. Readarr, LazyLibrarian, Mylar
		}

		u, ok := parseLinkedAppURL(fieldString(item.Fields, "baseUrl"))
		if !ok {
			continue // unparsable, wrong scheme, or no base URL configured: nothing to suggest
		}

		apiKey := fieldString(item.Fields, "apiKey")
		keySource := ""
		if apiKey == "" || apiKey == maskedFieldValue {
			apiKey = ""
		} else {
			keySource = "Prowlarr: " + shape.Clip(item.Name)
		}

		candidates = append(candidates, adapter.Candidate{
			Type:      def.Type,
			Name:      shape.Clip(item.Name),
			BaseURL:   u,
			APIKey:    apiKey,
			KeySource: keySource,
		})
	}
	return candidates, nil
}

// parseLinkedAppURL parses raw as a candidate's BaseURL: it must be http or https with a host,
// and is stripped of any userinfo, query or fragment, which have no business in a base URL and
// must not be carried into a suggestion unexamined.
func parseLinkedAppURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, false
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	return u, true
}

// fieldString returns the string value of the field named name, or "" if there is none or it is
// not a string.
func fieldString(fields []applicationField, name string) string {
	for _, f := range fields {
		if f.Name == name {
			s, _ := f.Value.(string)
			return s
		}
	}
	return ""
}

// definitionByAppName returns the Definitions entry whose AppName matches implementation
// case-insensitively.
func definitionByAppName(implementation string) (Definition, bool) {
	for _, d := range Definitions {
		if strings.EqualFold(d.AppName, implementation) {
			return d, true
		}
	}
	return Definition{}, false
}

// configXML is the subset of a *arr config.xml every family member shares.
type configXML struct {
	XMLName      xml.Name `xml:"Config"`
	Port         int      `xml:"Port"`
	UrlBase      string   `xml:"UrlBase"`
	ApiKey       string   `xml:"ApiKey"`
	EnableSsl    bool     `xml:"EnableSsl"`
	SslPort      int      `xml:"SslPort"`
	InstanceName string   `xml:"InstanceName"`
}

// ReadConfig returns a candidate if dir holds this app's config.xml: it is the same file format
// for every family member, so InstanceName (when the app writes one) or, failing that, name (the
// config folder's own base name, e.g. "sonarr" from a compose mount like
// /srv/sonarr/config:/appconfig/sonarr:ro) is what tells them apart. A custom InstanceName like
// "Anime Sonarr" or "Sonarr 4K" is still this app: the match is a case-insensitive substring, not
// equality, and when it matches, the InstanceName (not the generic app name) becomes the
// candidate's display Name, since the user chose it to tell their instances apart.
func (a arrAdapter) ReadConfig(name string, dir fs.FS) (adapter.Candidate, bool, error) {
	body, err := adapter.ReadConfigFile(dir, "config.xml")
	if errors.Is(err, fs.ErrNotExist) {
		return adapter.Candidate{}, false, nil
	}
	if err != nil {
		// Shown to the user after the folder's name; it names config.xml already, e.g. "config.xml is not a
		// regular file", "config.xml exceeds 1048576 bytes" or "config.xml is a symbolic link, not read".
		return adapter.Candidate{}, false, err
	}

	var cfg configXML
	if xml.Unmarshal(body, &cfg) != nil {
		// Deliberately drop the decoder's own message, which can echo back fragments of the
		// file, exactly like GetJSON does for a body that fails to decode as JSON.
		return adapter.Candidate{}, false, errors.New("arr: config.xml is not valid XML")
	}

	matched := false
	candidateName := a.def.Name
	if cfg.InstanceName != "" {
		matched = strings.Contains(strings.ToLower(cfg.InstanceName), strings.ToLower(a.def.AppName))
		if matched {
			candidateName = shape.Clip(cfg.InstanceName)
		}
	} else {
		matched = strings.Contains(strings.ToLower(name), strings.ToLower(a.def.AppName))
	}
	if !matched {
		return adapter.Candidate{}, false, nil
	}

	scheme := "http"
	port := cfg.Port
	if cfg.EnableSsl {
		scheme = "https"
		if cfg.SslPort != 0 {
			port = cfg.SslPort
		}
	}
	host := ""
	if port != 0 {
		host = ":" + strconv.Itoa(port)
	}
	candidateURL := &url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   withLeadingSlash(strings.TrimSuffix(cfg.UrlBase, "/")),
	}

	keySource := "config.xml"
	if name != "" {
		keySource = "config.xml in " + name
	}

	return adapter.Candidate{
		Type:      a.def.Type,
		Name:      candidateName,
		BaseURL:   candidateURL,
		APIKey:    cfg.ApiKey,
		KeySource: keySource,
	}, true, nil
}

func withLeadingSlash(p string) string {
	if p == "" {
		return "/"
	}
	if strings.HasPrefix(p, "/") {
		return p
	}
	return "/" + p
}

// MatchesImage recognises the common images of this app: the LinuxServer.io and hotio builds on Docker Hub, lscr.io
// and ghcr.io, and a bare "<type>" repository (an official or locally built image).
func (a arrAdapter) MatchesImage(image string) bool {
	t := a.def.Type
	switch adapter.ImageRepo(image) {
	case t, "linuxserver/" + t, "lscr.io/linuxserver/" + t, "ghcr.io/linuxserver/" + t, "hotio/" + t, "ghcr.io/hotio/" + t:
		return true
	}
	return false
}
