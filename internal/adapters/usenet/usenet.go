// Package usenet implements the Observarr adapters for two Usenet downloaders, SABnzbd (sabnzbd.go) and NZBGet
// (nzbget.go). Both report the same two shapes the same way: a health reading with the version, the app's own
// warnings, a paused downloader, low disk space and the downloads that failed in the last day; and the download
// queue with its speed. Neither adapter changes anything in the app.
package usenet

import (
	"bytes"
	"cmp"
	"embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/shape"
)

// icons holds each app's logo as icons/<type>.svg: one colour, its alpha the glyph. The logos belong to their
// projects and are used only to identify the apps (see NOTICE).
//
//go:embed icons/*.svg
var icons embed.FS

func logo(typ string) []byte {
	b, err := icons.ReadFile("icons/" + typ + ".svg")
	if err != nil {
		return nil
	}
	return b
}

// lowDisk is the free space below which a health reading warns: a single download is often several gigabytes.
const lowDisk = 1 << 30

// failedWindow is how far back health readings count failed downloads.
const failedWindow = 24 * time.Hour

// pausedWarning is the note for a downloader that is paused: a state someone chose, not a problem, so it is only
// information. Shown under the app's name, it does not repeat it.
var pausedWarning = shape.Warning{Level: shape.LevelInfo, Message: "downloads are paused"}

// diskWarning warns when free bytes, as an app reports them for its download folders, are below lowDisk.
func diskWarning(free int64) (shape.Warning, bool) {
	if free < 0 || free >= lowDisk {
		return shape.Warning{}, false
	}
	return shape.Warning{Level: shape.LevelWarning,
		Message: fmt.Sprintf("disk space low: %.1f GiB free for downloads", float64(free)/(1<<30))}, true
}

// failures counts the downloads that failed in the last day, newest first, and keeps the newest one's name and
// reason for the warning.
type failures struct {
	cutoff       int64 // unix seconds: failures before it do not count
	n            int
	name, reason string
	atLeast      bool // the app's list was cut off while still in the window: there may be more
}

func newFailures(now time.Time) failures { return failures{cutoff: now.Add(-failedWindow).Unix()} }

// add counts one failure that ended at the unix time at, and reports whether it was in the window.
func (f *failures) add(at int64, name, reason string) bool {
	if at < f.cutoff {
		return false
	}
	if f.n == 0 {
		f.name, f.reason = name, reason
	}
	f.n++
	return true
}

// warning is the health warning for f, if any failed: "Sintel.2010.1080p.WEB failed in the last day: Repair
// failed", or "3 downloads failed in the last day, most recently Sintel.2010.1080p.WEB: Repair failed".
func (f failures) warning() (shape.Warning, bool) {
	if f.n == 0 {
		return shape.Warning{}, false
	}
	var msg string
	if f.n == 1 && !f.atLeast {
		msg = cmp.Or(f.name, "1 download") + " failed in the last day"
	} else {
		msg = strconv.Itoa(f.n) + " download"
		if f.n != 1 {
			msg += "s"
		}
		if f.atLeast {
			msg += " or more"
		}
		msg += " failed in the last day"
		if f.name != "" {
			msg += ", most recently " + f.name
		}
	}
	if f.reason != "" {
		msg += ": " + f.reason
	}
	return shape.Warning{Level: shape.LevelWarning, Message: shape.Clip(msg)}, true
}

// firstLine is s up to its first line break, trimmed: an app's message without a stack trace below it.
func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(s)
}

// clock renders seconds as "h:mm:ss", the way SABnzbd shows time left.
func clock(sec int64) string {
	return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
}

// progress is the share of size already done, given what is left, clamped to [0, 1].
func progress(size, left float64) float64 {
	if size <= 0 {
		return 0
	}
	return min(1, max(0, (size-left)/size))
}

// fingerprintErr is what Fingerprint returns for err: err itself only when no answer came at all, as an answer of
// any kind (including a redirect elsewhere, as from a sign-in gateway) just means "not this app".
func fingerprintErr(err error) error {
	if _, unreachable := adapter.Describe(err); unreachable && !errors.Is(err, adapter.ErrCrossHostRedirect) {
		return err
	}
	return nil
}

// configValues reads the keys of one section of an INI-like config file ("key = value" lines; "[section]" starts a
// section, "[[sub]]" a subsection of it, "#" and ";" a comment). section "" is the part before any section, which
// is all of a file without sections. Keys are matched without regard to case and the first occurrence wins; a
// value loses surrounding quotes.
func configValues(body []byte, section string, keys ...string) map[string]string {
	out := make(map[string]string, len(keys))
	current := ""
	for line := range bytes.Lines(body) {
		s := strings.TrimSpace(string(line))
		switch {
		case s == "" || s[0] == '#' || s[0] == ';':
			continue
		case s[0] == '[':
			current = "\x00" // a subsection, or anything else, is none of the sections asked for
			if !strings.HasPrefix(s, "[[") && strings.HasSuffix(s, "]") {
				current = strings.ToLower(strings.TrimSpace(s[1 : len(s)-1]))
			}
			continue
		case current != strings.ToLower(section):
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		for _, want := range keys {
			if _, seen := out[want]; !seen && k == strings.ToLower(want) {
				v = strings.TrimSpace(v)
				if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
					v = v[1 : len(v)-1]
				}
				out[want] = v
			}
		}
	}
	return out
}

// configPort is a port from a config file, or 0 when it is not one.
func configPort(s string) int {
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return 0
	}
	return p
}
