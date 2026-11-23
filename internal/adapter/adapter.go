// Package adapter defines how Observarr talks to one app type. Everything app-specific lives behind Adapter.
package adapter

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"observarr/internal/shape"
)

// Info describes an adapter for the UI's "Add service" form.
type Info struct {
	Type        string       `json:"type"`        // stable id, e.g. "myapp"; stored with each service
	Name        string       `json:"name"`        // display name, e.g. "My App"
	DefaultPort int          `json:"defaultPort"` // suggested in the form
	NeedsAPIKey bool         `json:"needsApiKey"`
	Kinds       []shape.Kind `json:"kinds"` // shapes this adapter can fill; the UI shows only these
	Icon        string       `json:"icon"`  // icon name hint, e.g. "tv"; the UI falls back to a generic icon
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

// Errors an Adapter (or GetJSON, on its behalf) returns from Detect or Probe. Callers may match
// them with errors.Is.
var (
	ErrWrongApp     = errors.New("a different app answers at this address")
	ErrUnauthorized = errors.New("the app rejected the API key")
	ErrUnsupported  = errors.New("this app version is not supported")
	ErrUnknownKind  = errors.New("shape not supported by this adapter")
	ErrNotJSON      = errors.New("the response is not JSON")
	ErrNotFound     = errors.New("the app has no such endpoint")
	ErrTooLarge     = errors.New("the answer is too large")
)

// WrongApp returns the error for another app answering where this one was expected: one wrapping ErrWrongApp
// that reads "<app> answers at this address", with app (as that app names itself) cut to printable characters
// and 64 bytes. Without a usable name it is ErrWrongApp itself.
func WrongApp(app string) error {
	app = shape.ClipTo(strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, app)), 64)
	if app == "" {
		return ErrWrongApp
	}
	return wrongAppError(app)
}

type wrongAppError string

func (e wrongAppError) Error() string { return string(e) + " answers at this address" }
func (wrongAppError) Unwrap() error   { return ErrWrongApp }

// StatusError is an HTTP status an app answered with that none of the errors above stands for.
type StatusError int

func (e StatusError) Error() string { return "unexpected status " + strconv.Itoa(int(e)) }

// MaxURL bounds a base URL, in bytes.
const MaxURL = 2048

// ParseBaseURL checks a base URL as a user or a label gives it: http or https, a host, optionally a port and path,
// no user name, query or fragment, at most MaxURL bytes. It is returned without a trailing slash.
func ParseBaseURL(s string) (*url.URL, error) {
	if len(s) > MaxURL {
		return nil, errors.New("url: at most 2048 bytes")
	}
	u, err := url.Parse(s)
	// A literal "?" or "#" can only start a query or fragment; checking the text also catches empty ones.
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		strings.ContainsAny(s, "?#") {
		return nil, errors.New("url: http:// or https://, a host, optionally a port and path; " +
			"no user name, query or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u, nil
}
