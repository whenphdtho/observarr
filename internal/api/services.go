package api

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"observarr/internal/adapter"
	"observarr/internal/poller"
	"observarr/internal/shape"
	"observarr/internal/state"
	"observarr/internal/store"
	"observarr/internal/system"
)

const (
	maxServiceName = 64  // characters
	maxAPIKey      = 256 // bytes
	detectTimeout  = 10 * time.Second
)

// containerName is a Docker container name as a service may be linked to; "" means no link.
var containerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

// errKeyUnreadable is shown for a saved key the box cannot open, e.g. after the key file was replaced.
var errKeyUnreadable = errors.New("API key cannot be decrypted; enter it again")

// errKeyMissing is shown for an app that needs a key when none is saved, e.g. after the saved keys were dropped.
var errKeyMissing = errors.New("API key missing; enter it again")

// targetInput is where an app answers and with which key: what Detect checks.
type targetInput struct {
	Type   string `json:"type"`
	URL    string `json:"url"`
	APIKey string `json:"apiKey"`
}

// serviceInput is a complete service definition.
type serviceInput struct {
	targetInput
	Name      string `json:"name"`
	Container string `json:"container"` // linked Docker container name; "" = none
}

// servicePatch changes a service. An absent field stays as it is; an empty apiKey removes the key.
type servicePatch struct {
	Name      *string `json:"name"`
	URL       *string `json:"url"`
	APIKey    *string `json:"apiKey"`
	Container *string `json:"container"` // "" removes the link
}

// spec is a validated service definition.
type spec struct {
	name      string // empty for a bare target
	container string
	adapter   adapter.Adapter
	target    adapter.Target
}

func (s *Server) adapters(w http.ResponseWriter, _ *http.Request, _ store.Session) {
	writeJSON(w, http.StatusOK, s.Registry.Infos())
}

func (s *Server) services(w http.ResponseWriter, r *http.Request, _ store.Session) {
	writeBulkJSON(w, r, http.StatusOK, s.Hub.Snapshot())
}

// testService checks a target without saving anything and answers with the app's version.
func (s *Server) testService(w http.ResponseWriter, r *http.Request, _ store.Session) {
	var in targetInput
	if !readJSON(w, r, &in) {
		return
	}
	sp, err := s.checkTarget(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id, msg := s.detect(r.Context(), sp)
	if msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	writeJSON(w, http.StatusOK, id)
}

// createInput is a new service, optionally added from a discovery suggestion.
type createInput struct {
	serviceInput
	Suggestion  string `json:"suggestion"`  // suggestion id; type, URL and name still come from the body
	UseFoundKey bool   `json:"useFoundKey"` // without an apiKey in the body, use the key the suggestion found
}

// createService saves a service once the app has confirmed it is what the user says, then polls it.
func (s *Server) createService(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var in createInput
	if !readJSON(w, r, &in) {
		return
	}
	keySource := ""
	if in.Suggestion != "" {
		// Peek, not Take: a failed check below leaves the suggestion for another try.
		c, ok := s.discovery.Peek(in.Suggestion)
		if !ok {
			writeError(w, http.StatusGone, errSuggestionGone)
			return
		}
		if in.Container == "" {
			in.Container = c.Container // a Docker find links its container
		}
		if in.UseFoundKey {
			if c.APIKey == "" {
				writeError(w, http.StatusBadRequest, "no API key was found for this suggestion")
				return
			}
			// A key found for one app is never sent to an app of another type.
			if in.Type != c.Type {
				writeError(w, http.StatusBadRequest, "type must match the suggestion to use its key")
				return
			}
			if in.APIKey == "" {
				if why := foundKeyRefusal(in.URL, c); why != "" {
					writeError(w, http.StatusBadRequest, why)
					return
				}
				in.APIKey, keySource = c.APIKey, c.KeySource
			}
		}
	} else if in.UseFoundKey {
		writeError(w, http.StatusBadRequest, "useFoundKey: only with a suggestion")
		return
	}
	host := ""
	if u, err := url.Parse(in.URL); err == nil {
		host = u.Host
	}
	// fail answers with msg; a failed add that was to use a found key is audited, like a successful one.
	fail := func(code int, msg string) {
		if keySource != "" {
			s.audit(r, sess.UserName, "add from suggestion failed", shape.Clip(in.Type+" "+host+": "+msg))
		}
		writeError(w, code, msg)
	}
	sp, err := s.validate(in.serviceInput)
	if err != nil {
		fail(http.StatusBadRequest, err.Error())
		return
	}
	if _, msg := s.detect(r.Context(), sp); msg != "" {
		fail(http.StatusUnprocessableEntity, msg)
		return
	}

	s.servicesMu.Lock()
	defer s.servicesMu.Unlock()
	// Checked again under the lock: of two adds of one suggestion, only the first is saved.
	if in.Suggestion != "" {
		if _, ok := s.discovery.Peek(in.Suggestion); !ok {
			fail(http.StatusGone, errSuggestionGone)
			return
		}
	}
	if twin, err := s.twin(r.Context(), in.Type, sp.target.BaseURL, 0); err != nil {
		s.internal(w, r, err)
		return
	} else if twin != "" {
		fail(http.StatusConflict, twin)
		return
	}
	svc, err := s.Store.CreateService(r.Context(),
		store.Service{Type: in.Type, Name: sp.name, BaseURL: sp.target.BaseURL.String(), Container: sp.container},
		func(id int64) ([]byte, error) { return s.sealKey(id, sp.target.APIKey) })
	if errors.Is(err, store.ErrExists) {
		fail(http.StatusConflict, "a service with this name already exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Not cancelled with the request: the service is saved, so its suggestions must go too.
	ctx := context.WithoutCancel(r.Context())
	if in.Suggestion != "" {
		if _, err := s.discovery.Take(ctx, in.Suggestion); err != nil {
			s.Log.Warn("added suggestion may be suggested again", "service", svc.Name, "err", err)
		}
	}
	// Other finds of the same app: at the same address, or without one but with the same key.
	if err := s.discovery.Forget(ctx, svc.Type, sp.target.BaseURL, sp.target.APIKey); err != nil {
		s.Log.Warn("added app may be suggested again", "service", svc.Name, "err", err)
	}
	st := s.run(svc, sp.target)
	detail := svc.Name + " (" + svc.Type + ")"
	if keySource != "" {
		detail += "; key from " + keySource + " sent to " + sp.target.BaseURL.Host
	}
	s.audit(r, sess.UserName, "added service", detail)
	writeJSON(w, http.StatusCreated, s.current(st))
}

// updateService applies a patch. A changed URL or key must pass Detect again; the poller restarts with the
// result either way, so it always polls what is saved. Detect runs without servicesMu, so a slow app does not
// hold up other changes; the save then only goes ahead if the service is still as it was read.
func (s *Server) updateService(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var p servicePatch
	if !readJSON(w, r, &p) {
		return
	}
	old, ok := s.serviceByID(w, r)
	if !ok {
		return
	}
	oldKey, keyErr := s.openKey(old)
	if keyErr != nil && p.APIKey == nil {
		writeError(w, http.StatusUnprocessableEntity, keyErr.Error())
		return
	}
	in := serviceInput{targetInput{Type: old.Type, URL: old.BaseURL, APIKey: oldKey}, old.Name, old.Container}
	if p.Name != nil {
		in.Name = *p.Name
	}
	if p.URL != nil {
		in.URL = *p.URL
	}
	if p.APIKey != nil {
		in.APIKey = *p.APIKey
	}
	if p.Container != nil {
		in.Container = *p.Container
	}
	sp, err := s.validate(in)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A saved key goes only to the address it was entered for: moving it to another host needs it again.
	oldURL, err := url.Parse(old.BaseURL)
	if oldKey != "" && p.APIKey == nil && (err != nil || origin(oldURL) != origin(sp.target.BaseURL)) {
		label := cmp.Or(strings.ToLower(sp.adapter.Info().KeyLabel), "API key")
		writeError(w, http.StatusBadRequest, "enter the "+label+" again when changing the address")
		return
	}
	if twin, err := s.twin(r.Context(), old.Type, sp.target.BaseURL, old.ID); err != nil {
		s.internal(w, r, err)
		return
	} else if twin != "" {
		writeError(w, http.StatusConflict, twin)
		return
	}
	keyChanged := keyErr != nil || in.APIKey != oldKey
	updated := store.Service{ID: old.ID, Type: old.Type, Name: sp.name, BaseURL: sp.target.BaseURL.String(),
		SealedKey: old.SealedKey, Container: sp.container}
	if updated.BaseURL != old.BaseURL || keyChanged {
		if _, msg := s.detect(r.Context(), sp); msg != "" {
			writeError(w, http.StatusUnprocessableEntity, msg)
			return
		}
	}
	if keyChanged {
		if updated.SealedKey, err = s.sealKey(old.ID, in.APIKey); err != nil {
			s.internal(w, r, err)
			return
		}
	}

	s.servicesMu.Lock()
	defer s.servicesMu.Unlock()
	cur, ok := s.serviceByID(w, r)
	if !ok {
		return
	}
	if cur.Name != old.Name || cur.BaseURL != old.BaseURL || cur.Container != old.Container ||
		!bytes.Equal(cur.SealedKey, old.SealedKey) {
		writeError(w, http.StatusConflict, "the service was changed meanwhile; reload and try again")
		return
	}
	switch err := s.Store.UpdateService(r.Context(), updated); {
	case errors.Is(err, store.ErrExists):
		writeError(w, http.StatusConflict, "a service with this name already exists")
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	st := s.run(updated, sp.target)
	detail := updated.Name
	if updated.BaseURL != old.BaseURL {
		detail += ": " + old.BaseURL + " → " + updated.BaseURL
	}
	s.audit(r, sess.UserName, "changed service", detail)
	writeJSON(w, http.StatusOK, s.current(st))
}

// deleteService stops polling a service, then forgets it.
func (s *Server) deleteService(w http.ResponseWriter, r *http.Request, sess store.Session) {
	s.servicesMu.Lock()
	defer s.servicesMu.Unlock()
	svc, ok := s.serviceByID(w, r)
	if !ok {
		return
	}
	s.Poller.Stop(svc.ID)
	if err := s.Store.DeleteService(r.Context(), svc.ID); err != nil {
		s.startSaved(svc) // it is still saved, so keep polling it
		s.internal(w, r, err)
		return
	}
	s.Hub.Remove(svc.ID)
	s.audit(r, sess.UserName, "removed service", svc.Name)
	w.WriteHeader(http.StatusNoContent)
}

// StartServices shows every saved service in the hub and starts polling it. Call it once, before serving.
func (s *Server) StartServices() error {
	svcs, err := s.Store.Services(s.Context)
	if err != nil {
		return err
	}
	for _, svc := range svcs {
		s.startSaved(svc)
	}
	return nil
}

// startSaved shows a saved service in the hub and polls it with its saved key. A service whose key cannot be
// opened, or whose key was dropped (OBSERVARR_RESET_KEYS), shows why instead and is not polled until the key is
// entered again.
func (s *Server) startSaved(svc store.Service) {
	key, err := s.openKey(svc)
	if a, ok := s.Registry.Get(svc.Type); ok && err == nil && key == "" && a.Info().NeedsAPIKey {
		err = errKeyMissing
	}
	var u *url.URL
	if err == nil {
		u, err = url.Parse(svc.BaseURL)
	}
	if err != nil {
		s.Log.Warn("service not polled", "service", svc.Name, "err", err)
		s.Hub.Put(s.stateOf(svc))
		s.Hub.Record(svc.ID, shape.KindHealth, shape.Reading{Error: err.Error(), At: time.Now()})
		return
	}
	s.run(svc, adapter.Target{BaseURL: u, APIKey: key})
}

// run shows svc in the hub and (re)starts its poller loop for t. Put comes first: the hub drops readings of
// services it does not hold.
func (s *Server) run(svc store.Service, t adapter.Target) state.ServiceState {
	st := s.stateOf(svc)
	s.Hub.Put(st)
	s.Poller.Start(s.Context, poller.Service{ID: svc.ID, Type: svc.Type, BaseURL: t.BaseURL, APIKey: t.APIKey})
	return st
}

// stateOf is the hub's metadata for svc. A type without adapter (e.g. one dropped from the catalog) shows its
// health only, where the poller reports the unknown type.
func (s *Server) stateOf(svc store.Service) state.ServiceState {
	kinds := []shape.Kind{shape.KindHealth}
	if a, ok := s.Registry.Get(svc.Type); ok {
		kinds = a.Info().Kinds
	}
	return state.ServiceState{ID: svc.ID, Type: svc.Type, Name: svc.Name, URL: svc.BaseURL, HasKey: len(svc.SealedKey) > 0,
		Kinds: kinds, Readings: map[shape.Kind]shape.Reading{}, Container: brief(svc.Container, s.systemSnapshot().Containers)}
}

// brief is what the service card shows of container name, from the sampler's containers; nil for no link.
func brief(name string, cs []system.ContainerView) *state.ContainerBrief {
	if name == "" {
		return nil
	}
	b := &state.ContainerBrief{Name: name}
	for _, c := range cs {
		if c.Name == name {
			b.State, b.Health, b.Restarts = c.State, c.Health, c.RestartCount
			// Finer changes are noise on a card, and each change is a live event.
			b.CPUPercent = math.Round(c.CPUPercent*10) / 10
			b.MemBytes = uint64(math.Round(float64(c.MemBytes)/(1<<20))) << 20
			break
		}
	}
	return b
}

// followContainers keeps linked services' container briefs current: on every sampler snapshot it recomputes them
// and puts the services whose brief changed into the hub. It runs until the server's context ends.
func (s *Server) followContainers() {
	snaps, cancel := s.System.Subscribe()
	defer cancel()
	for {
		select {
		case <-s.Context.Done():
			return
		case snap := <-snaps:
			s.updateBriefs(snap.Containers)
		}
	}
}

func (s *Server) updateBriefs(cs []system.ContainerView) {
	// Under servicesMu, so a service changed meanwhile is never put back with its old metadata.
	s.servicesMu.Lock()
	defer s.servicesMu.Unlock()
	for _, st := range s.Hub.Snapshot() {
		if st.Container == nil {
			continue
		}
		if b := brief(st.Container.Name, cs); *b != *st.Container {
			st.Container = b
			s.Hub.Put(st)
		}
	}
}

// current returns the hub's state of st's service, readings included, or st itself if the hub lacks it.
func (s *Server) current(st state.ServiceState) state.ServiceState {
	for _, c := range s.Hub.Snapshot() {
		if c.ID == st.ID {
			return c
		}
	}
	return st
}

// serviceByID loads the service named by the {id} path value, answering 404 or 500 itself when it cannot.
func (s *Server) serviceByID(w http.ResponseWriter, r *http.Request) (store.Service, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such service")
		return store.Service{}, false
	}
	svc, err := s.Store.ServiceByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such service")
		return store.Service{}, false
	}
	if err != nil {
		s.internal(w, r, err)
		return store.Service{}, false
	}
	return svc, true
}

// keyContext binds a sealed key to its service row, so it cannot be moved to another one.
func keyContext(id int64) string { return "service:" + strconv.FormatInt(id, 10) }

// sealKey seals key for service id; no key is stored as nil.
func (s *Server) sealKey(id int64, key string) ([]byte, error) {
	if key == "" {
		return nil, nil
	}
	return s.Box.Seal([]byte(key), keyContext(id))
}

// openKey returns svc's key, "" if it has none, or errKeyUnreadable.
func (s *Server) openKey(svc store.Service) (string, error) {
	if svc.SealedKey == nil {
		return "", nil
	}
	key, err := s.Box.Open(svc.SealedKey, keyContext(svc.ID))
	if err != nil {
		return "", errKeyUnreadable
	}
	return string(key), nil
}

// validate checks a complete service definition.
func (s *Server) validate(in serviceInput) (spec, error) {
	sp, err := s.checkTarget(in.targetInput)
	if err != nil {
		return spec{}, err
	}
	return sp.named(in.Name, in.Container)
}

// named checks a service's name and container link and adds them to sp.
func (sp spec) named(name, container string) (spec, error) {
	sp.name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(sp.name); n < 1 || n > maxServiceName {
		return spec{}, errors.New("name: 1-64 characters")
	}
	if strings.ContainsFunc(sp.name, unicode.IsControl) {
		return spec{}, errors.New("name: no line breaks or other control characters")
	}
	if container != "" && !containerName.MatchString(container) {
		return spec{}, errors.New("container: a Docker container name (letters, digits, _ . -), at most 255 characters")
	}
	sp.container = container
	return sp, nil
}

// checkTarget checks the type, URL and key. The URL is returned without a trailing slash.
func (s *Server) checkTarget(in targetInput) (spec, error) {
	sp, err := s.parseTarget(in)
	if err == nil && in.APIKey == "" && sp.adapter.Info().NeedsAPIKey {
		return spec{}, errors.New("apiKey: required for " + sp.adapter.Info().Name)
	}
	return sp, err
}

// parseTarget is checkTarget without requiring a key, for an app whose key is to be entered later.
func (s *Server) parseTarget(in targetInput) (spec, error) {
	a, ok := s.Registry.Get(in.Type)
	if !ok {
		return spec{}, errors.New("type: unknown app type")
	}
	u, err := adapter.ParseBaseURL(in.URL)
	if err != nil {
		return spec{}, err
	}
	if len(in.APIKey) > maxAPIKey {
		return spec{}, errors.New("apiKey: at most 256 bytes")
	}
	// A control character could never be a real key, and in a header it fails only once the request is made.
	if strings.ContainsFunc(in.APIKey, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return spec{}, errors.New("apiKey: no control characters")
	}
	return spec{adapter: a, target: adapter.Target{BaseURL: u, APIKey: in.APIKey, HTTP: s.HTTP}}, nil
}

// detect asks the app to confirm sp. When it does not, it returns why, for a 422.
func (s *Server) detect(ctx context.Context, sp spec) (adapter.Identity, string) {
	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	id, err := sp.adapter.Detect(ctx, sp.target)
	if err != nil {
		// Refused without anything to refuse: say what is missing, not that it was rejected.
		if errors.Is(err, adapter.ErrUnauthorized) && sp.target.APIKey == "" {
			if strings.EqualFold(sp.adapter.Info().KeyLabel, "Credentials") {
				return adapter.Identity{}, "the app asks for credentials: enter them as user:password"
			}
			return adapter.Identity{}, "the app asks for a key: enter its " + cmp.Or(strings.ToLower(sp.adapter.Info().KeyLabel), "API key")
		}
		return adapter.Identity{}, detectError(sp.target.BaseURL.Host, err)
	}
	return id, ""
}

// twin says, for a refusal, which saved service other than except already is the app of typ at u, "" for none:
// one app watched twice would show and report everything twice.
func (s *Server) twin(ctx context.Context, typ string, u *url.URL, except int64) (string, error) {
	saved, err := s.Store.Services(ctx)
	if err != nil {
		return "", err
	}
	for _, svc := range saved {
		other, err := url.Parse(svc.BaseURL)
		if svc.ID == except || svc.Type != typ || err != nil {
			continue
		}
		if origin(other) == origin(u) && strings.TrimSuffix(other.Path, "/") == strings.TrimSuffix(u.Path, "/") {
			return "this app is already added as " + strconv.Quote(svc.Name), nil
		}
	}
	return "", nil
}

// detectError explains a failed Detect. Adapter errors never contain the key (adapter.GetJSON guarantees it),
// and a network error is reduced to its cause, without the request URL.
func detectError(host string, err error) string {
	switch phrase, unreachable := adapter.Describe(err); {
	case unreachable:
		return "cannot reach " + host + ": " + phrase
	case phrase != "":
		return phrase
	}
	return err.Error()
}

// foundKeyRefusal says why c's found key may not be sent to rawURL, "" when it may. A find with an address has
// its key sent there only. A find from a config folder knows the app's port but not its host, which the user
// fills in: the port must be the config's, and the host this server, a name on its network (the container's,
// say, which has no dot) or a private address, never a public one. Whoever is signed in could else have any
// found key sent to a server of their choosing. A URL that does not parse is left for validation to refuse.
func foundKeyRefusal(rawURL string, c adapter.Candidate) string {
	u, err := adapter.ParseBaseURL(rawURL)
	if err != nil {
		return ""
	}
	if c.BaseURL.Hostname() != "" {
		if origin(u) != origin(c.BaseURL) {
			return "url: the found key is only sent to the address where the app was found"
		}
		return ""
	}
	if want := portOf(c.BaseURL); portOf(u) != want {
		return "url: the found key is only sent to the port its config names (" + want + "); enter the key by hand for another"
	}
	if !nearby(u.Hostname(), c) {
		return "url: the found key is only sent to this server or its own network, not to a public address; " +
			"enter the key by hand to use this one"
	}
	return ""
}

// nearby reports whether host is this server or on its own network: a loopback or private address, "localhost",
// the name the find suggests, or a name without a dot (a container or a machine next door). A full domain name
// may be anyone's.
func nearby(host string, c adapter.Candidate) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsLoopback() || ip.IsPrivate()
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host != "" && (!strings.Contains(host, ".") || host == strings.ToLower(c.Container) || host == strings.ToLower(c.Name))
}

// portOf is the port a request to u goes to, the scheme's own when u names none.
func portOf(u *url.URL) string {
	return cmp.Or(u.Port(), map[string]string{"http": "80", "https": "443"}[u.Scheme])
}

// origin is u's scheme, host and port, with the scheme's default port filled in: where a request to u goes.
func origin(u *url.URL) string {
	return u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), portOf(u))
}
