// Package jellyfin implements the Observarr adapter for Jellyfin. It reads, with an API key, the server's version
// and state and the sessions playing something, and nothing else.
//
// The field names below were checked against Jellyfin's own source (github.com/jellyfin/jellyfin, master as of
// November 2026). Jellyfin writes JSON in PascalCase, and a flags enum as an array of its names
// (src/Jellyfin.Extensions/Json: JsonDefaults, JsonFlagEnumConverter).
//
//   - GET /System/Info/Public (MediaBrowser.Model/System/PublicSystemInfo.cs): "ProductName" ("Jellyfin Server"),
//     "Version", "ServerName", "Id". Needs no key.
//   - GET /System/Info (SystemInfo.cs, which extends it): "HasPendingRestart", "IsShuttingDown" and
//     "HasUpdateAvailable" (obsolete; Jellyfin leaves it false). Needs a key.
//   - GET /Sessions (MediaBrowser.Model/Dto/SessionInfoDto.cs): a list of sessions with "UserName", "Client",
//     "DeviceName", "PlayState" (Session/PlayerStateInfo.cs: "PositionTicks", "IsPaused", "PlayMethod" of
//     DirectPlay, DirectStream or Transcode, "AudioStreamIndex"), "NowPlayingItem" (a BaseItemDto: "Name",
//     "Type", "SeriesName", "ParentIndexNumber", "IndexNumber", "ProductionYear", "AlbumArtist", "RunTimeTicks",
//     and "MediaStreams" of the source played, each with "Type", "Codec", "BitRate", "Index"; see
//     SessionManager.GetItemInfo) and "TranscodingInfo" (Session/TranscodingInfo.cs: "VideoCodec",
//     "AudioCodec", "IsVideoDirect", "IsAudioDirect", "Bitrate", "TranscodeReasons"). A session also carries
//     the client's address ("RemoteEndPoint"); this adapter never reads it.
//
// The key goes in the Authorization header in the "MediaBrowser" scheme, the form Jellyfin supports going
// forward; the X-Emby-Token header is deprecated and off by default from Jellyfin 12.
//
// Emby shares the API's origins but not enough of it to be covered by the same code: its public system info has
// no product name to recognise it by, and it authenticates differently.
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// Type is the adapter's stable id.
const Type = "jellyfin"

// The calls this adapter makes. None changes anything.
const (
	pathPublicInfo = "/System/Info/Public"
	pathInfo       = "/System/Info"
	// The server's own dashboard lists the sessions active in the last 16 minutes; a session playing something
	// reports its progress every few seconds, so it is always among them.
	pathSessions = "/Sessions?activeWithinSeconds=960"
)

const (
	// maxSessions bounds the sessions read from one answer; any beyond are not counted.
	maxSessions = 1000
	// maxSessionBytes bounds the answer listing them: each session carries its item's details.
	maxSessionBytes = 16 << 20
)

// ErrKey is returned for a key that cannot be sent in the Authorization header.
var ErrKey = errors.New("api key: letters, digits and dashes only")

type jfAdapter struct{}

// New returns the Jellyfin adapter.
func New() adapter.Adapter { return jfAdapter{} }

// Info describes Jellyfin for the "Add apps" page.
func (jfAdapter) Info() adapter.Info {
	return adapter.Info{
		Type:        Type,
		Name:        "Jellyfin",
		DefaultPort: 8096,
		NeedsAPIKey: true,
		Kinds:       []shape.Kind{shape.KindHealth, shape.KindStreams},
		Icon:        "tv",
		KeyHint:     "Dashboard → API Keys",
	}
}

// authorized is t with its key in the form the Authorization header takes, for adapter.StreamJSON to send as it
// is. A key is a token of letters and digits; anything that could break out of the quotes is refused.
func authorized(t adapter.Target) (adapter.Target, error) {
	if t.APIKey == "" || strings.ContainsFunc(t.APIKey, func(r rune) bool {
		return r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-')
	}) {
		return t, ErrKey
	}
	t.APIKey = `MediaBrowser Token="` + t.APIKey + `"`
	return t, nil
}

// get GETs path with the key and decodes the answer into v.
func get(ctx context.Context, t adapter.Target, path string, v any) error {
	t, err := authorized(t)
	if err != nil {
		return err
	}
	return adapter.GetJSON(ctx, t, path, "Authorization", v)
}

// publicInfo is the relevant subset of /System/Info/Public.
type publicInfo struct {
	ProductName string `json:"ProductName"`
	Version     string `json:"Version"`
}

// fetchPublic reads /System/Info/Public without a key and reports whether Jellyfin answers it. Something that
// answers otherwise is ErrWrongApp, or names itself if it gives a product name.
func fetchPublic(ctx context.Context, t adapter.Target) (publicInfo, error) {
	var p publicInfo
	t.APIKey = ""
	err := adapter.GetJSON(ctx, t, pathPublicInfo, "", &p)
	var status adapter.StatusError
	switch {
	// Jellyfin answers this without a key: a refusal means something else answers. A server error is an error.
	case errors.Is(err, adapter.ErrNotJSON), errors.Is(err, adapter.ErrNotFound),
		errors.Is(err, adapter.ErrUnauthorized), errors.As(err, &status) && status < 500:
		return p, adapter.ErrWrongApp
	case err != nil:
		return p, err
	case !strings.HasPrefix(p.ProductName, "Jellyfin"):
		return p, adapter.WrongApp(p.ProductName)
	}
	return p, nil
}

// systemInfo is the relevant subset of /System/Info.
type systemInfo struct {
	Version            string `json:"Version"`
	HasPendingRestart  bool   `json:"HasPendingRestart"`
	IsShuttingDown     bool   `json:"IsShuttingDown"`
	HasUpdateAvailable bool   `json:"HasUpdateAvailable"`
}

func fetchInfo(ctx context.Context, t adapter.Target) (systemInfo, time.Duration, error) {
	var s systemInfo
	start := time.Now()
	err := get(ctx, t, pathInfo, &s)
	return s, time.Since(start), err
}

// Detect confirms Jellyfin by its public system info, then the key by the full one.
func (jfAdapter) Detect(ctx context.Context, t adapter.Target) (adapter.Identity, error) {
	if _, err := authorized(t); err != nil {
		return adapter.Identity{}, err
	}
	if _, err := fetchPublic(ctx, t); err != nil {
		return adapter.Identity{}, err
	}
	s, _, err := fetchInfo(ctx, t)
	if err != nil {
		return adapter.Identity{}, err
	}
	return adapter.Identity{Version: shape.Clip(s.Version)}, nil
}

// Fingerprint recognises Jellyfin without a key by its public system info, which also gives its version. Only a
// failure to get an answer at all is an error; any answer that is not Jellyfin's is just not a match.
func (jfAdapter) Fingerprint(ctx context.Context, t adapter.Target) (bool, string, error) {
	p, err := fetchPublic(ctx, t)
	switch {
	case err == nil:
		return true, shape.Clip(p.Version), nil
	case errors.Is(err, adapter.ErrWrongApp), errors.Is(err, adapter.ErrTooLarge),
		errors.Is(err, adapter.ErrCrossHostRedirect):
		return false, "", nil
	}
	return false, "", err
}

// MatchesImage recognises the official image and the LinuxServer.io and hotio builds.
func (jfAdapter) MatchesImage(image string) bool {
	switch adapter.ImageRepo(image) {
	case "jellyfin/jellyfin", "ghcr.io/jellyfin/jellyfin", "linuxserver/jellyfin", "lscr.io/linuxserver/jellyfin",
		"ghcr.io/linuxserver/jellyfin", "hotio/jellyfin", "ghcr.io/hotio/jellyfin":
		return true
	}
	return false
}

// Probe fills one declared shape kind.
func (jfAdapter) Probe(ctx context.Context, t adapter.Target, kind shape.Kind) (any, error) {
	switch kind {
	case shape.KindHealth:
		return probeHealth(ctx, t)
	case shape.KindStreams:
		return probeStreams(ctx, t)
	default:
		return nil, adapter.ErrUnknownKind
	}
}

// probeHealth reports the version and latency, and what the server says of its own state.
func probeHealth(ctx context.Context, t adapter.Target) (any, error) {
	s, latency, err := fetchInfo(ctx, t)
	if err != nil {
		return nil, err
	}
	h := shape.Health{Version: shape.Clip(s.Version), LatencyMS: latency.Milliseconds(), Warnings: []shape.Warning{}}
	if s.IsShuttingDown {
		h.Warnings = append(h.Warnings, shape.Warning{Level: shape.LevelWarning, Message: "the server is shutting down"})
	}
	if s.HasPendingRestart {
		h.Warnings = append(h.Warnings, shape.Warning{Level: shape.LevelWarning,
			Message: "a restart is pending to apply changes"})
	}
	if s.HasUpdateAvailable {
		h.Warnings = append(h.Warnings, shape.Warning{Level: shape.LevelInfo, Message: "a new version is available"})
	}
	return h, nil
}

// session is the relevant subset of one entry of /Sessions. It has no field for the client's address, so that is
// never read.
type session struct {
	UserName   string `json:"UserName"`
	Client     string `json:"Client"`
	DeviceName string `json:"DeviceName"`
	PlayState  *struct {
		PositionTicks    int64  `json:"PositionTicks"`
		IsPaused         bool   `json:"IsPaused"`
		PlayMethod       string `json:"PlayMethod"`
		AudioStreamIndex *int   `json:"AudioStreamIndex"`
	} `json:"PlayState"`
	NowPlayingItem  *item `json:"NowPlayingItem"`
	TranscodingInfo *struct {
		VideoCodec       string          `json:"VideoCodec"`
		AudioCodec       string          `json:"AudioCodec"`
		IsVideoDirect    bool            `json:"IsVideoDirect"`
		IsAudioDirect    bool            `json:"IsAudioDirect"`
		Bitrate          int64           `json:"Bitrate"`
		TranscodeReasons json.RawMessage `json:"TranscodeReasons"`
	} `json:"TranscodingInfo"`
}

// item is the relevant subset of the item a session plays.
type item struct {
	Name              string        `json:"Name"`
	Type              string        `json:"Type"` // Movie, Episode, Audio, …
	SeriesName        string        `json:"SeriesName"`
	ParentIndexNumber *int          `json:"ParentIndexNumber"` // an episode's season
	IndexNumber       *int          `json:"IndexNumber"`
	ProductionYear    int           `json:"ProductionYear"`
	AlbumArtist       string        `json:"AlbumArtist"`
	RunTimeTicks      int64         `json:"RunTimeTicks"`
	MediaStreams      []mediaStream `json:"MediaStreams"`
}

// mediaStream is one stream of the source a session plays.
type mediaStream struct {
	Type    string `json:"Type"` // Video, Audio, Subtitle, …
	Codec   string `json:"Codec"`
	BitRate int64  `json:"BitRate"`
	Index   int    `json:"Index"`
}

// probeStreams reads the sessions and reports the ones playing something.
func probeStreams(ctx context.Context, t adapter.Target) (any, error) {
	t, err := authorized(t)
	if err != nil {
		return nil, err
	}
	var all []shape.Stream
	err = adapter.StreamJSONLimit(ctx, t, pathSessions, "Authorization", maxSessionBytes, func(d *json.Decoder) error {
		_, err := adapter.DecodeArray(d, maxSessions, func(s session) {
			if st, ok := stream(s); ok {
				all = append(all, st)
			}
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	out := shape.Streams{Total: len(all), Streams: []shape.Stream{}}
	for _, s := range all {
		if s.Paused {
			out.Paused++
		}
		if s.Method == shape.PlayTranscode {
			out.Transcodes++
		}
		out.Bitrate += s.Bitrate // each at most maxBitrate, and the sessions are bounded: no overflow
	}
	// The server lists the most recently active session first, which changes from one reading to the next; by
	// user and title, the list keeps still.
	slices.SortStableFunc(all, func(a, b shape.Stream) int {
		if c := strings.Compare(strings.ToLower(a.User), strings.ToLower(b.User)); c != 0 {
			return c
		}
		return strings.Compare(a.Title, b.Title)
	})
	out.Streams = append(out.Streams, all[:min(len(all), shape.MaxStreams)]...)
	return out, nil
}

// stream is what s plays, if anything.
func stream(s session) (shape.Stream, bool) {
	it := s.NowPlayingItem
	if it == nil {
		return shape.Stream{}, false
	}
	out := shape.Stream{
		User:    shape.Clip(s.UserName),
		Client:  shape.Clip(s.Client),
		Device:  shape.Clip(s.DeviceName),
		Title:   shape.Clip(title(it)),
		Reasons: []string{},
	}
	audioIndex := -1
	if ps := s.PlayState; ps != nil {
		out.Paused = ps.IsPaused
		if it.RunTimeTicks > 0 {
			out.Progress = min(1, max(0, float64(ps.PositionTicks)/float64(it.RunTimeTicks)))
		}
		if ps.AudioStreamIndex != nil {
			audioIndex = *ps.AudioStreamIndex
		}
		out.Method = method(ps.PlayMethod, s.TranscodingInfo != nil && s.TranscodingInfo.IsVideoDirect)
	}
	video, audio := sourceStreams(it.MediaStreams, audioIndex)
	out.VideoCodec, out.AudioCodec = shape.ClipTo(video.Codec, 32), shape.ClipTo(audio.Codec, 32)
	out.Bitrate = bitrate(video.BitRate) + bitrate(audio.BitRate)
	if ti := s.TranscodingInfo; ti != nil && out.Method != shape.PlayDirect && out.Method != "" {
		if !ti.IsVideoDirect {
			out.ToVideoCodec = shape.ClipTo(ti.VideoCodec, 32)
		}
		if !ti.IsAudioDirect {
			out.ToAudioCodec = shape.ClipTo(ti.AudioCodec, 32)
		}
		if ti.Bitrate > 0 {
			out.Bitrate = bitrate(ti.Bitrate)
		}
		out.Reasons = reasons(ti.TranscodeReasons)
	}
	return out, true
}

// maxBitrate caps a bitrate the server reports (1 Tbit/s, far above any stream), so the sums of them cannot
// overflow.
const maxBitrate = 1 << 40

// bitrate is b, a bitrate the server reports, between 0 and maxBitrate.
func bitrate(b int64) int64 { return min(max(b, 0), maxBitrate) }

// title names an item: "Series S01E02 · Episode", "Film (2008)", "Artist · Track", or just its name.
func title(it *item) string {
	switch {
	case it.Type == "Episode" && it.SeriesName != "":
		s := it.SeriesName
		if it.ParentIndexNumber != nil && it.IndexNumber != nil {
			s += " S" + pad(*it.ParentIndexNumber) + "E" + pad(*it.IndexNumber)
		}
		if it.Name != "" {
			s += " · " + it.Name
		}
		return s
	case it.Type == "Movie" && it.ProductionYear > 0:
		return it.Name + " (" + strconv.Itoa(it.ProductionYear) + ")"
	case it.Type == "Audio" && it.AlbumArtist != "":
		return it.AlbumArtist + " · " + it.Name
	}
	return it.Name
}

// pad is a season or episode number with at least two digits.
func pad(n int) string {
	if n >= 0 && n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// method is a session's PlayMethod as a shape play method. Jellyfin reports a remux, and a stream whose audio
// alone is converted, as Transcode; with the video sent as it is, that is a direct stream.
func method(playMethod string, videoDirect bool) string {
	switch playMethod {
	case "DirectPlay":
		return shape.PlayDirect
	case "DirectStream":
		return shape.PlayDirectStream
	case "Transcode":
		if videoDirect {
			return shape.PlayDirectStream
		}
		return shape.PlayTranscode
	}
	return ""
}

// sourceStreams picks the video stream and the audio stream played (the one at audioIndex, else the first) from
// the source's streams.
func sourceStreams(streams []mediaStream, audioIndex int) (video, audio mediaStream) {
	var haveVideo, haveAudio bool
	for _, m := range streams {
		switch m.Type {
		case "Video":
			if !haveVideo {
				video, haveVideo = m, true
			}
		case "Audio":
			if !haveAudio || m.Index == audioIndex {
				audio, haveAudio = m, true
			}
		}
	}
	return video, audio
}

// reasonText words the reasons that the generic rule (split the name into lower-case words) words badly.
var reasonText = map[string]string{
	"ContainerBitrateExceedsLimit": "bitrate too high",
	"VideoBitrateNotSupported":     "video bitrate too high",
	"AudioBitrateNotSupported":     "audio bitrate too high",
	"StreamCountExceedsLimit":      "too many streams",
	"AudioIsExternal":              "external audio track",
	"SecondaryAudioNotSupported":   "second audio track not supported",
	"RefFramesNotSupported":        "reference frames not supported",
	"UnknownVideoStreamInfo":       "video stream unknown",
	"UnknownAudioStreamInfo":       "audio stream unknown",
	"DirectPlayError":              "direct play failed",
}

// reasons reads TranscodeReasons, an array of reason names (or, from older servers, one comma-separated string),
// as at most MaxStreamReasons phrases.
func reasons(raw json.RawMessage) []string {
	var names []string
	if json.Unmarshal(raw, &names) != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return []string{}
		}
		names = strings.Split(s, ",")
	}
	out := []string{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || len(out) == shape.MaxStreamReasons {
			continue
		}
		text, ok := reasonText[n]
		if !ok {
			text = words(n)
		}
		if text = shape.ClipTo(text, 64); !slices.Contains(out, text) {
			out = append(out, text)
		}
	}
	return out
}

// words splits a PascalCase name into lower-case words: "AudioCodecNotSupported" is "audio codec not supported".
func words(name string) string {
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}
