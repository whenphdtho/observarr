package discovery

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/collect"
	"observarr/internal/store"
)

const (
	maxProbes      = 128                     // network probes in flight at once
	probeTimeout   = 1500 * time.Millisecond // one Fingerprint call
	scanTimeout    = 120 * time.Second       // one whole network scan or Docker run
	linkedTimeout  = 10 * time.Second        // one LinkedApps call
	maxFolders     = 50                      // config folders read per run
	maxSourceErrs  = 100                     // errors reported per run
	suggestionTTL  = time.Hour
	maxSuggestions = 500
)

// ErrNoSuggestion is returned for a suggestion id that is unknown or has expired.
var ErrNoSuggestion = errors.New("discovery: no such suggestion (it may have expired)")

// Suggestion is what the UI shows. Key material stays inside the Engine: there is deliberately no
// field that could carry it.
type Suggestion struct {
	ID        string `json:"id"` // random, unguessable
	Type      string `json:"type"`
	Name      string `json:"name"`
	URL       string `json:"url"` // may be "http://:8989/" style when the host is unknown → UI asks for it
	HostKnown bool   `json:"hostKnown"`
	Version   string `json:"version,omitempty"`
	KeyFound  bool   `json:"keyFound"`
	KeySource string `json:"keySource,omitempty"`
	Source    string `json:"source"` // "network" | "linked" | "config" | "docker" (Local reports the last two)
	// Verified is false for a Docker find whose app could not be recognised at its URL; Reason says why.
	Verified  bool   `json:"verified"`
	Reason    string `json:"reason,omitempty"`
	Container string `json:"container,omitempty"` // the Docker container, for Docker finds
}

// SourceError is one thing a source could not check. Message is short and never holds a key or a
// response body.
type SourceError struct {
	Target  string `json:"target"`
	Message string `json:"message"`
}

// Result is one run's outcome: what is new, and what could not be checked.
type Result struct {
	Suggestions []Suggestion  `json:"suggestions"`
	Errors      []SourceError `json:"errors"`
}

// LinkedSource is a registered service the Linked source may query (decrypted key included).
type LinkedSource struct {
	Type    string
	BaseURL *url.URL
	APIKey  string
}

// entry is a stored suggestion together with the candidate (and key) it stands for.
type entry struct {
	s       Suggestion
	c       adapter.Candidate
	key     string // dedupe and dismissal key
	seq     uint64 // insertion order, for dropping the oldest
	expires time.Time
}

// Engine runs discovery sources and keeps their suggestions in memory for a while. Nothing it
// finds is added, polled or stored as a service; the API does that when the user adds one.
type Engine struct {
	reg   *adapter.Registry
	httpc *http.Client
	st    *store.Store
	now   func() time.Time
	log   *slog.Logger

	probeTimeout, scanTimeout time.Duration // the constants above; tests shorten them

	mu      sync.Mutex
	entries map[string]*entry
	seq     uint64
	skipped map[string]bool // container names already logged as unusable
}

// maxSkipped bounds the unusable container names remembered; beyond, all are forgotten (and logged again).
const maxSkipped = 1000

// New returns an Engine with no suggestions.
func New(reg *adapter.Registry, httpc *http.Client, st *store.Store, now func() time.Time, log *slog.Logger) *Engine {
	return &Engine{reg: reg, httpc: httpc, st: st, now: now, log: log, probeTimeout: probeTimeout,
		scanTimeout: scanTimeout, entries: make(map[string]*entry), skipped: make(map[string]bool)}
}

// find is one candidate a source produced.
type find struct {
	c          adapter.Candidate
	version    string
	unverified string // why the app could not be recognised; "" when it was, or the source read it from the app
	source     string // the source that found it, when a run combines several; "" = the run's own
}

// Reasons a Docker find is not verified.
const (
	reasonUnreachable = "not reachable from Observarr (different Docker network?)"
	reasonOtherApp    = "something else answers at this address"
	reasonUnchecked   = "not checked: this app cannot be recognised without its key"
	reasonNotYet      = "not contacted yet: Observarr checks it when you add it"
)

// ScanNetwork probes http://host:<DefaultPort> of every adapter that implements
// adapter.Fingerprinter, for every host (from ParseTargets). Probes sharing a host and port run one
// adapter after another, and the first that recognises the app wins; see probe for errors.
func (e *Engine) ScanNetwork(ctx context.Context, hosts []string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, e.scanTimeout)
	defer cancel()

	var ports []int
	byPort := make(map[int][]adapter.Adapter)
	for _, a := range e.adapters() {
		if _, ok := a.(adapter.Fingerprinter); ok {
			p := a.Info().DefaultPort
			if byPort[p] == nil {
				ports = append(ports, p)
			}
			byPort[p] = append(byPort[p], a)
		}
	}

	var (
		mu    sync.Mutex
		finds []find
		errs  []SourceError
		wg    sync.WaitGroup
		slots = make(chan struct{}, maxProbes)
	)
scan:
	for _, h := range hosts {
		for _, p := range ports {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				break scan
			}
			wg.Go(func() {
				defer func() { <-slots }()
				f, perrs := e.probe(ctx, net.JoinHostPort(h, strconv.Itoa(p)), byPort[p])
				mu.Lock()
				defer mu.Unlock()
				if f != nil {
					finds = append(finds, *f)
				}
				errs = append(errs, perrs...)
			})
		}
	}
	wg.Wait()
	if ctx.Err() != nil {
		errs = append(errs, SourceError{Target: "network", Message: "time limit (120 s) reached — scan a smaller range or list hosts"})
	}
	return e.keep(ctx, "network", finds, errs, false)
}

// probe fingerprints hostPort with each of adapters in turn until one recognises the app. When
// nothing answers there at all (refused, unreachable, timed out), it stops without an error: no
// other adapter would get an answer either. Any other failure is recorded (once per distinct
// message) and the next adapter still gets its try, since another app may answer differently.
func (e *Engine) probe(ctx context.Context, hostPort string, adapters []adapter.Adapter) (*find, []SourceError) {
	base := &url.URL{Scheme: "http", Host: hostPort, Path: "/"}
	var errs []SourceError
	for _, a := range adapters {
		pctx, cancel := context.WithTimeout(ctx, e.probeTimeout)
		ok, version, err := a.(adapter.Fingerprinter).Fingerprint(pctx, adapter.Target{BaseURL: base, HTTP: e.httpc})
		cancel()
		switch {
		case err != nil:
			if ctx.Err() != nil || nothingThere(err) {
				return nil, errs
			}
			serr := SourceError{Target: hostPort, Message: shortError(err)}
			if !slices.Contains(errs, serr) {
				errs = append(errs, serr)
			}
		case ok:
			info := a.Info()
			return &find{c: adapter.Candidate{Type: info.Type, Name: info.Name, BaseURL: base}, version: version}, errs
		}
	}
	return nil, errs
}

// nothingThere reports whether err just means that nothing answers at the address — the normal
// outcome for most addresses of a scanned range, which would drown real problems if reported.
func nothingThere(err error) bool {
	var netErr net.Error
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.EHOSTDOWN) || errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout()
}

// shortError reduces err to a short cause, without the request URL or anything an app sent.
func shortError(err error) string {
	if phrase, _ := adapter.Describe(err); phrase != "" {
		return phrase
	}
	return "request failed"
}

// Linked asks every source whose adapter implements adapter.Linker for the apps it is connected to.
// Candidates of a type no adapter is registered for are dropped.
func (e *Engine) Linked(ctx context.Context, sources []LinkedSource) (Result, error) {
	var (
		finds []find
		errs  []SourceError
	)
	for _, src := range sources {
		a, ok := e.reg.Get(src.Type)
		linker, isLinker := a.(adapter.Linker)
		if !ok || !isLinker {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, linkedTimeout)
		cands, err := linker.LinkedApps(lctx, adapter.Target{BaseURL: src.BaseURL, APIKey: src.APIKey, HTTP: e.httpc})
		cancel()
		if err != nil {
			errs = append(errs, SourceError{Target: src.BaseURL.Host, Message: shortError(err)})
			continue
		}
		for _, c := range cands {
			if _, known := e.reg.Get(c.Type); known && c.BaseURL != nil {
				finds = append(finds, find{c: c})
			}
		}
	}
	return e.keep(ctx, "linked", finds, errs, false)
}

// ConfigFolders reads every immediate subdirectory of the folder root (at most 50, in name order) with each
// adapter that implements adapter.ConfigReader; the first that recognises a folder wins. A folder that does not
// answer before ctx ends (a hung network mount, say) ends the run with an error for it.
func (e *Engine) ConfigFolders(ctx context.Context, root string) (Result, error) {
	finds, errs, err := e.configFinds(ctx, root)
	if err != nil {
		return Result{}, err
	}
	return e.keep(ctx, "config", finds, errs, false)
}

// configFinds reads the config folders under root for ConfigFolders and Local. It only reads files: no app is
// contacted.
func (e *Engine) configFinds(ctx context.Context, root string) ([]find, []SourceError, error) {
	type listing struct {
		dirents []fs.DirEntry
		err     error
	}
	list, ok := await(ctx, func() listing {
		dirents, err := os.ReadDir(root)
		return listing{dirents, err}
	})
	if !ok {
		return nil, []SourceError{{Target: filepath.Base(root), Message: errNoAnswer}}, nil
	}
	if list.err != nil {
		return nil, nil, list.err
	}
	var readers []adapter.ConfigReader
	for _, a := range e.adapters() {
		if r, ok := a.(adapter.ConfigReader); ok {
			readers = append(readers, r)
		}
	}

	var (
		finds   []find
		errs    []SourceError
		folders int
	)
	for _, d := range list.dirents {
		if d.Type()&fs.ModeSymlink != 0 {
			// Not followed: a mounted config folder is a real directory, and a link could lead
			// anywhere in the container's file system.
			errs = append(errs, SourceError{Target: d.Name(), Message: "symbolic link, not read"})
			continue
		}
		if !d.IsDir() {
			continue
		}
		if folders == maxFolders {
			errs = append(errs, SourceError{Target: d.Name(),
				Message: "only the first " + strconv.Itoa(maxFolders) + " folders are read"})
			break
		}
		if ctx.Err() != nil {
			break
		}
		folders++
		dir := adapter.ConfigDir(filepath.Join(root, d.Name()))
		type reading struct {
			c   adapter.Candidate
			err error // the first error, when no reader recognises the folder
		}
		r, ok := await(ctx, func() (res reading) {
			for _, reader := range readers {
				c, ok, err := reader.ReadConfig(d.Name(), dir)
				if ok && c.BaseURL != nil {
					return reading{c: c}
				}
				if res.err == nil {
					res.err = err
				}
			}
			return res
		})
		switch {
		case !ok:
			errs = append(errs, SourceError{Target: d.Name(), Message: errNoAnswer})
		case r.c.BaseURL != nil:
			finds = append(finds, find{c: r.c})
		case r.err != nil:
			errs = append(errs, SourceError{Target: d.Name(), Message: r.err.Error()})
		}
	}
	return finds, errs, nil
}

const errNoAnswer = "the folder did not answer in time"

// await runs f in a goroutine of its own and waits for it until ctx ends (ok false). A file system that stopped
// answering, such as a hung network mount, then leaves f blocked in the background instead of the run.
func await[T any](ctx context.Context, f func() T) (v T, ok bool) {
	done := make(chan T, 1)
	go func() { done <- f() }()
	select {
	case v = <-done:
		return v, true
	case <-ctx.Done():
		return v, false
	}
}

// Docker suggests the app of every running container that names a registered type in its "observarr.type" label,
// or else whose image an adapter recognises (adapter.ImageMatcher; the first in the registry's order wins). Its URL
// is http://<container name>:<port>, the port being the adapter's default port when the container exposes it, else
// its first TCP port; an "observarr.url" label replaces the URL. Each find is fingerprinted where the adapter allows
// it; one that cannot be recognised there is still suggested, marked unverified with the reason. A container whose
// name is not a valid Docker name is skipped.
func (e *Engine) Docker(ctx context.Context, cs []collect.Container) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, e.scanTimeout)
	defer cancel()
	apps, errs := e.containerApps(cs)

	var (
		mu    sync.Mutex
		finds []find
		wg    sync.WaitGroup
		slots = make(chan struct{}, maxProbes)
	)
	for _, ca := range apps {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			errs = append(errs, SourceError{Target: "docker", Message: "the run stopped before every container was checked"})
			break
		}
		wg.Go(func() {
			defer func() { <-slots }()
			f := e.verify(ctx, ca.a, ca.c)
			mu.Lock()
			finds = append(finds, f)
			mu.Unlock()
		})
	}
	wg.Wait()
	return e.keep(ctx, "docker", finds, errs, false)
}

// containerApp is a running container an adapter recognises, with the candidate it stands for.
type containerApp struct {
	a     adapter.Adapter
	c     adapter.Candidate
	label bool // the URL comes from the container's "observarr.url" label
}

// containerApps returns the recognised running containers of cs, in order, without contacting any of them.
func (e *Engine) containerApps(cs []collect.Container) ([]containerApp, []SourceError) {
	var matchers []adapter.Adapter
	for _, a := range e.adapters() {
		if _, ok := a.(adapter.ImageMatcher); ok {
			matchers = append(matchers, a)
		}
	}
	var (
		out  []containerApp
		errs []SourceError
	)
	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		if !containerName.MatchString(c.Name) {
			e.logSkipped(c.Name)
			continue
		}
		a := e.containerApp(c, matchers)
		if a == nil {
			continue
		}
		base, err := containerURL(c, a.Info().DefaultPort)
		if err != nil {
			errs = append(errs, SourceError{Target: c.Name, Message: err.Error()})
			continue
		}
		_, label := c.Labels["observarr.url"]
		out = append(out, containerApp{a: a, label: label,
			c: adapter.Candidate{Type: a.Info().Type, Name: a.Info().Name, BaseURL: base, Container: c.Name}})
	}
	return out, errs
}

// Local lists the apps found on this server without contacting any of them: every running container of cs an
// adapter recognises (as Docker does, but not fingerprinted, so each is unverified) and every config folder under
// root (as ConfigFolders; root "" skips them). A host-less config find with a key is joined to the container of its
// type when each is the only one of that type and the ports agree: the suggestion then has the container's address
// and the folder's key, and the API sends that key to that address only. Earlier suggestions for the same app keep
// their id, so a list the UI already shows stays valid when it lists again.
func (e *Engine) Local(ctx context.Context, cs []collect.Container, root string) (Result, error) {
	apps, errs := e.containerApps(cs)
	var cfinds []find
	if root != "" {
		found, cerrs, err := e.configFinds(ctx, root)
		if err != nil {
			cerrs = append([]SourceError{{Target: filepath.Base(root), Message: "the config folder cannot be read"}}, cerrs...)
		}
		cfinds = found
		errs = append(errs, cerrs...)
	}

	containers := make(map[string][]int) // per type, the containers found with a computed address
	for i, ca := range apps {
		if !ca.label {
			containers[ca.c.Type] = append(containers[ca.c.Type], i)
		}
	}
	hostless := make(map[string]int) // per type, the host-less config finds
	for _, f := range cfinds {
		if f.c.BaseURL.Hostname() == "" {
			hostless[f.c.Type]++
		}
	}
	finds := make([]find, 0, len(apps)+len(cfinds))
	joined := make(map[int]bool)
	for _, f := range cfinds {
		is := containers[f.c.Type]
		if f.c.BaseURL.Hostname() != "" || f.c.APIKey == "" || len(is) != 1 || hostless[f.c.Type] != 1 ||
			apps[is[0]].c.BaseURL.Port() != f.c.BaseURL.Port() {
			f.source = "config"
			finds = append(finds, f)
			continue
		}
		c := apps[is[0]].c
		c.BaseURL = cloneURL(c.BaseURL)
		c.BaseURL.Path = cmp.Or(f.c.BaseURL.Path, "/")
		c.Name = cmp.Or(f.c.Name, c.Name)
		c.APIKey, c.KeySource = f.c.APIKey, f.c.KeySource
		finds = append(finds, find{c: c, unverified: reasonNotYet, source: "docker"})
		joined[is[0]] = true
	}
	for i, ca := range apps {
		if !joined[i] {
			finds = append(finds, find{c: ca.c, unverified: reasonNotYet, source: "docker"})
		}
	}
	return e.keep(ctx, "local", finds, errs, true)
}

// containerName matches the names Docker gives containers, at most 63 characters (a DNS label). Anything else
// (only a misbehaving Docker endpoint sends it) could put another host or user info into the URL built from it.
var containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// logSkipped notes at debug level, once per name, that a container was skipped for its name.
func (e *Engine) logSkipped(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.skipped[name] {
		return
	}
	if len(e.skipped) >= maxSkipped {
		clear(e.skipped)
	}
	e.skipped[name] = true
	e.log.Debug("skipping container with an unusable name", "name", name)
}

// containerApp returns the adapter for c: the one its "observarr.type" label names, if registered, else the first
// that recognises its image, else nil.
func (e *Engine) containerApp(c collect.Container, matchers []adapter.Adapter) adapter.Adapter {
	if a, ok := e.reg.Get(c.Labels["observarr.type"]); ok {
		return a
	}
	for _, a := range matchers {
		if a.(adapter.ImageMatcher).MatchesImage(c.Image) {
			return a
		}
	}
	return nil
}

// containerURL is where the app in c answers on c's Docker network, or what its "observarr.url" label says.
func containerURL(c collect.Container, defaultPort int) (*url.URL, error) {
	if label, ok := c.Labels["observarr.url"]; ok {
		u, err := adapter.ParseBaseURL(label)
		if err != nil {
			return nil, errors.New("observarr.url label: " + err.Error())
		}
		return u, nil
	}
	port := 0
	for _, p := range c.Ports {
		if p.Proto == "tcp" && (p.Private == defaultPort || port == 0) {
			port = p.Private
		}
	}
	if port == 0 {
		port = defaultPort
	}
	return &url.URL{Scheme: "http", Host: net.JoinHostPort(c.Name, strconv.Itoa(port)), Path: "/"}, nil
}

// verify fingerprints c's app at its URL, where the adapter can.
func (e *Engine) verify(ctx context.Context, a adapter.Adapter, c adapter.Candidate) find {
	f := find{c: c}
	fp, ok := a.(adapter.Fingerprinter)
	if !ok {
		f.unverified = reasonUnchecked
		return f
	}
	pctx, cancel := context.WithTimeout(ctx, e.probeTimeout)
	defer cancel()
	switch match, version, err := fp.Fingerprint(pctx, adapter.Target{BaseURL: c.BaseURL, HTTP: e.httpc}); {
	case err != nil:
		f.unverified = reasonUnreachable
	case !match:
		f.unverified = reasonOtherApp
	default:
		f.version = version
	}
	return f
}

// adapters returns every registered adapter in the registry's order (by name).
func (e *Engine) adapters() []adapter.Adapter {
	infos := e.reg.Infos()
	out := make([]adapter.Adapter, 0, len(infos))
	for _, info := range infos {
		a, _ := e.reg.Get(info.Type)
		out = append(out, a)
	}
	return out
}

// keep drops finds that are already registered services, dismissed, or duplicates, stores the
// rest as suggestions and returns them sorted by type, then URL. A fresh find replaces an earlier
// suggestion for the same app; with reuse it keeps that suggestion's id.
func (e *Engine) keep(ctx context.Context, source string, finds []find, errs []SourceError, reuse bool) (Result, error) {
	// The run's own context may have ended (a timed-out scan still returns what it found).
	ctx = context.WithoutCancel(ctx)
	svcs, err := e.st.Services(ctx)
	if err != nil {
		return Result{}, err
	}
	skip, err := e.st.Dismissed(ctx)
	if err != nil {
		return Result{}, err
	}
	linked := make(map[string]bool) // containers a service is linked to already
	for _, svc := range svcs {
		if u, err := url.Parse(svc.BaseURL); err == nil {
			skip[svc.Type+" "+normalizeURL(u)] = true
		}
		if svc.Container != "" {
			linked[svc.Container] = true
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.prune()
	res := Result{Suggestions: []Suggestion{}, Errors: errs}
	if res.Errors == nil {
		res.Errors = []SourceError{}
	}
	if len(res.Errors) > maxSourceErrs {
		res.Errors = res.Errors[:maxSourceErrs]
	}
	for _, f := range finds {
		key := dismissKey(f.c)
		if skip[key] || linked[f.c.Container] {
			continue
		}
		skip[key] = true // once per run
		id := ""
		for oldID, old := range e.entries {
			if old.key == key {
				if reuse && id == "" {
					id = oldID
				}
				delete(e.entries, oldID) // a fresh find replaces an earlier one
			}
		}
		if id == "" {
			id = newID()
		}
		ent := &entry{c: f.c, key: key, expires: e.now().Add(suggestionTTL)}
		ent.c.BaseURL = cloneURL(f.c.BaseURL)
		ent.s = Suggestion{ID: id, Type: f.c.Type, Name: f.c.Name, URL: f.c.BaseURL.String(),
			HostKnown: f.c.BaseURL.Hostname() != "", Version: f.version, KeyFound: f.c.APIKey != "",
			Source: cmp.Or(f.source, source), Verified: f.unverified == "", Reason: f.unverified, Container: f.c.Container}
		if ent.s.Name == "" {
			if a, ok := e.reg.Get(f.c.Type); ok {
				ent.s.Name = a.Info().Name
			}
		}
		if ent.s.KeyFound {
			ent.s.KeySource = f.c.KeySource
		}
		e.seq++
		ent.seq = e.seq
		e.entries[ent.s.ID] = ent
		res.Suggestions = append(res.Suggestions, ent.s)
	}
	e.trim()
	// A run larger than the cap keeps only what trim kept.
	res.Suggestions = slices.DeleteFunc(res.Suggestions, func(s Suggestion) bool { return e.entries[s.ID] == nil })
	sortSuggestions(res.Suggestions)
	return res, nil
}

// Suggestions returns every current suggestion, sorted by type, then URL.
func (e *Engine) Suggestions() []Suggestion {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prune()
	out := make([]Suggestion, 0, len(e.entries))
	for _, ent := range e.entries {
		out = append(out, ent.s)
	}
	sortSuggestions(out)
	return out
}

// Peek returns the candidate (key included) of suggestion id, leaving it in place.
func (e *Engine) Peek(id string) (adapter.Candidate, bool) {
	ent, ok := e.lookup(id, false)
	if !ok {
		return adapter.Candidate{}, false
	}
	return ent.candidate(), true
}

// Take is for a suggestion that was just added as a service: it removes suggestion id and returns
// its candidate (key included) only once, then ErrNoSuggestion. A find with a host needs nothing
// more: every run skips registered services' URLs, so once that service is deleted it can be
// suggested again. A host-less config-folder find matches no registered URL, so Take records it
// like Dismiss does, and it stays suppressed even after the service is deleted.
func (e *Engine) Take(ctx context.Context, id string) (adapter.Candidate, error) {
	ent, ok := e.lookup(id, true)
	if !ok {
		return adapter.Candidate{}, ErrNoSuggestion
	}
	if ent.c.BaseURL.Hostname() != "" {
		return ent.candidate(), nil
	}
	return ent.candidate(), e.st.Dismiss(ctx, ent.key)
}

// Forget removes every suggestion of type typ at u (compared normalised), e.g. once a service was
// added there by hand or from another suggestion. Host-less suggestions never match.
func (e *Engine) Forget(typ string, u *url.URL) {
	key := typ + " " + normalizeURL(u)
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, ent := range e.entries {
		if ent.key == key {
			delete(e.entries, id)
		}
	}
}

// Dismiss records that suggestion id should not be suggested again and removes it. It returns the
// dismissed suggestion, or ErrNoSuggestion. The record is permanent: neither a restart nor anything
// else makes it a suggestion again; the user can still add that app by hand.
func (e *Engine) Dismiss(ctx context.Context, id string) (Suggestion, error) {
	ent, ok := e.lookup(id, false)
	if !ok {
		return Suggestion{}, ErrNoSuggestion
	}
	if err := e.st.Dismiss(ctx, ent.key); err != nil {
		return Suggestion{}, err
	}
	e.mu.Lock()
	delete(e.entries, id)
	e.mu.Unlock()
	return ent.s, nil
}

// lookup returns the unexpired entry id, removing it if remove is set.
func (e *Engine) lookup(id string, remove bool) (*entry, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prune()
	ent, ok := e.entries[id]
	if ok && remove {
		delete(e.entries, id)
	}
	return ent, ok
}

// candidate returns a copy of ent's candidate that shares nothing with ent.
func (ent *entry) candidate() adapter.Candidate {
	c := ent.c
	c.BaseURL = cloneURL(c.BaseURL)
	return c
}

// prune removes expired entries. e.mu must be held.
func (e *Engine) prune() {
	now := e.now()
	for id, ent := range e.entries {
		if !now.Before(ent.expires) {
			delete(e.entries, id)
		}
	}
}

// trim drops the oldest entries beyond maxSuggestions. e.mu must be held.
func (e *Engine) trim() {
	if len(e.entries) <= maxSuggestions {
		return
	}
	all := make([]*entry, 0, len(e.entries))
	for _, ent := range e.entries {
		all = append(all, ent)
	}
	slices.SortFunc(all, func(a, b *entry) int { return cmp.Compare(a.seq, b.seq) })
	for _, ent := range all[:len(all)-maxSuggestions] {
		delete(e.entries, ent.s.ID)
	}
}

func sortSuggestions(s []Suggestion) {
	slices.SortFunc(s, func(a, b Suggestion) int {
		return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.URL, b.URL), cmp.Compare(a.ID, b.ID))
	})
}

// dismissKey identifies what a candidate stands for: "<type> <normalised url>". A config-folder
// find without a host also names where it was found, since two folders commonly hold the same
// app on the same port on different hosts.
func dismissKey(c adapter.Candidate) string {
	key := c.Type + " " + normalizeURL(c.BaseURL)
	if c.BaseURL.Hostname() == "" {
		key += " " + c.KeySource
	}
	return key
}

// normalizeURL is the one comparable form of a base URL: lower-case scheme and host, the port
// always explicit (80/443 when absent), no trailing slash.
func normalizeURL(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port) +
		strings.TrimRight(u.EscapedPath(), "/")
}

func cloneURL(u *url.URL) *url.URL {
	c := *u
	return &c
}

// newID returns 128 random bits, URL-safe.
func newID() string {
	b := make([]byte, 16)
	rand.Read(b) // never fails (crypto/rand: "Read ... never returns an error")
	return base64.RawURLEncoding.EncodeToString(b)
}
