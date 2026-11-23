package adapter

import (
	"context"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Candidate is something a discovery source found. It is only a suggestion until the user adds
// it: nothing in this package stores, polls or otherwise acts on a Candidate by itself.
type Candidate struct {
	Type      string   // adapter type
	BaseURL   *url.URL // may lack a host for config-folder finds (the user completes it)
	Name      string   // suggested display name
	APIKey    string   // found key, if any; never sent to the browser
	KeySource string   // where the key came from, shown to the user, e.g. "config.xml in <folder>" or "<app>: <name>"
	Container string   // the Docker container the app runs in, for finds from the container list
}

// Fingerprinter recognises the app without credentials (used by the network scan). An adapter
// implements it by adding a Fingerprint method; the discovery package finds it with a type
// assertion, so adapters that have no unauthenticated signal simply don't implement it.
type Fingerprinter interface {
	// Fingerprint reports whether t looks like this app, using only unauthenticated requests. It
	// returns the version when the app reveals it without a key, else "".
	Fingerprint(ctx context.Context, t Target) (ok bool, version string, err error)
}

// Linker lists other apps this app is connected to (e.g. the apps an indexer manager syncs to).
type Linker interface {
	LinkedApps(ctx context.Context, t Target) ([]Candidate, error)
}

// ConfigReader reads this app's settings from its config folder (mounted read-only into
// Observarr).
type ConfigReader interface {
	// ReadConfig returns a candidate if dir holds this app's config. name is the folder's base
	// name (e.g. "myapp" from a mount like /appconfig/myapp): a hint for apps whose config
	// carries no app-identifying field of its own, and shown back to the user as part of the
	// candidate's KeySource. ok=false means "not mine". dir is a ConfigDir: what it opens is
	// never a symbolic link and opening never waits, so a reader checks what it got with Stat on
	// the open file.
	ReadConfig(name string, dir fs.FS) (c Candidate, ok bool, err error)
}

// ConfigDir is a config folder on disk, as ConfigReader reads it. Its Open never follows a symbolic link in place
// of the file and never waits for a FIFO's writer or a device (O_NOFOLLOW|O_NONBLOCK). The folder itself is a
// mount the operator chose; what is inside it belongs to the app and may be swapped at any time.
type ConfigDir string

// Open opens name, a slash-separated path in d, for reading. An error names only name, as the user sees it after
// the folder's name and need not learn where the folder is on the server.
func (d ConfigDir) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	f, err := os.OpenFile(filepath.Join(string(d), filepath.FromSlash(name)), os.O_RDONLY|noFollowNonBlock, 0)
	var pe *fs.PathError
	switch {
	case err == nil:
		return f, nil
	case errors.Is(err, syscall.ELOOP): // what O_NOFOLLOW gives for a link
		return nil, errors.New(name + " is a symbolic link, not read")
	case errors.As(err, &pe):
		return nil, &fs.PathError{Op: pe.Op, Path: name, Err: pe.Err}
	}
	return nil, err
}

// ImageMatcher recognises the app's container images (used by Docker discovery).
type ImageMatcher interface {
	// MatchesImage reports whether a container running image (as Docker lists it, e.g.
	// "lscr.io/linuxserver/app:latest") runs this app.
	MatchesImage(image string) bool
}

// ImageRepo reduces an image reference to its lower-case repository: without tag, digest and registry port, and
// without Docker Hub's own registry names and "library/" prefix, so "docker.io/library/App:1@sha256:…" is "app" and
// "lscr.io:443/linuxserver/app" is "lscr.io/linuxserver/app".
func ImageRepo(image string) string {
	s := strings.ToLower(image)
	s, _, _ = strings.Cut(s, "@")
	if i := strings.LastIndexByte(s, ':'); i > strings.LastIndexByte(s, '/') {
		s = s[:i]
	}
	if first, rest, ok := strings.Cut(s, "/"); ok && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, _, _ := strings.Cut(first, ":")
		switch host {
		case "docker.io", "index.docker.io", "registry-1.docker.io":
			s = rest
		default:
			s = host + "/" + rest
		}
	}
	return strings.TrimPrefix(s, "library/")
}
