package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/collect"
	"observarr/internal/discovery"
	"observarr/internal/shape"
	"observarr/internal/store"
	"observarr/internal/system"
)

// discoveryTimeout bounds one discovery request, whatever the client does meanwhile. It lies above the discovery
// engine's own limit (120 s), so the engine's more helpful message comes first.
const discoveryTimeout = 150 * time.Second

type discoverySources struct {
	Network bool `json:"network"`
	Linked  bool `json:"linked"`
	Config  bool `json:"config"`
	Docker  bool `json:"docker"`
}

// discoveryState lists the sources that can run now and the current suggestions.
func (s *Server) discoveryState(w http.ResponseWriter, r *http.Request, _ store.Session) {
	svcs, err := s.Store.Services(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	linked := false
	for _, svc := range svcs {
		a, _ := s.Registry.Get(svc.Type)
		if _, ok := a.(adapter.Linker); ok {
			linked = true
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sources": discoverySources{Network: true, Linked: linked, Config: s.Config.AppConfigs != "",
			Docker: s.Config.DockerHost != ""},
		"suggestions": s.discovery.Suggestions(),
	})
}

func (s *Server) discoverNetwork(w http.ResponseWriter, r *http.Request, sess store.Session) {
	var in struct {
		Targets string `json:"targets"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	hosts, err := discovery.ParseTargets(in.Targets)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(hosts) == 0 {
		writeError(w, http.StatusBadRequest, "targets: enter at least one host or subnet")
		return
	}
	s.discover(w, r, sess, "ran network discovery", shape.Clip(in.Targets), func(ctx context.Context) (discovery.Result, error) {
		return s.discovery.ScanNetwork(ctx, hosts)
	})
}

// discoverLinked asks every registered service whose adapter is a Linker for its linked apps.
func (s *Server) discoverLinked(w http.ResponseWriter, r *http.Request, sess store.Session) {
	s.discover(w, r, sess, "ran linked-app discovery", "", func(ctx context.Context) (discovery.Result, error) {
		svcs, err := s.Store.Services(ctx)
		if err != nil {
			return discovery.Result{}, err
		}
		var (
			sources []discovery.LinkedSource
			keyErrs []discovery.SourceError
		)
		for _, svc := range svcs {
			a, _ := s.Registry.Get(svc.Type)
			if _, ok := a.(adapter.Linker); !ok {
				continue
			}
			key, err := s.openKey(svc)
			var u *url.URL
			if err == nil {
				u, err = url.Parse(svc.BaseURL)
			}
			if err != nil {
				keyErrs = append(keyErrs, discovery.SourceError{Target: svc.Name, Message: err.Error()})
				continue
			}
			sources = append(sources, discovery.LinkedSource{Type: svc.Type, BaseURL: u, APIKey: key})
		}
		res, err := s.discovery.Linked(ctx, sources)
		if err == nil && len(keyErrs) > 0 {
			res.Errors = append(keyErrs, res.Errors...)
		}
		return res, err
	})
}

// localTimeout bounds one listing of the apps on this server; only a hung config-folder mount takes that long.
const localTimeout = 10 * time.Second

// discoverLocal lists what this server shows of its apps without contacting any of them: the containers in the
// sampler's list an adapter recognises, and the config folders. It is what the Add apps view shows as soon as it
// opens, so it runs on its own, beside any discovery run, and is not audited: it reads, sends nothing anywhere and
// shows no key. A second listing while one is running answers 409.
func (s *Server) discoverLocal(w http.ResponseWriter, r *http.Request, _ store.Session) {
	if !s.listingLocal.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "the apps on this server are being listed already")
		return
	}
	defer s.listingLocal.Store(false)
	ctx, cancel := context.WithTimeout(r.Context(), localTimeout)
	defer cancel()
	var cs []collect.Container
	var errs []discovery.SourceError
	if s.Config.DockerHost != "" {
		snap := s.systemSnapshot()
		cs = make([]collect.Container, len(snap.Containers))
		for i, v := range snap.Containers {
			cs[i] = v.Container
		}
		for _, n := range snap.Notices { // Docker could not be read: say why no container is listed
			if n == system.NoticeDocker || n == system.NoticeDockerError {
				errs = append(errs, discovery.SourceError{Target: "docker", Message: n})
			}
		}
	}
	res, err := s.discovery.Local(ctx, cs, s.Config.AppConfigs)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	res.Errors = append(errs, res.Errors...)
	writeJSON(w, http.StatusOK, map[string]any{
		"sources":     discoverySources{Network: true, Config: s.Config.AppConfigs != "", Docker: s.Config.DockerHost != ""},
		"suggestions": res.Suggestions,
		"errors":      res.Errors,
	})
}

// discover runs one discovery source, one run at a time server-wide, under its own deadline: a
// scan the client walks away from still ends on time, and ends with the server.
func (s *Server) discover(w http.ResponseWriter, r *http.Request, sess store.Session, action, detail string,
	run func(context.Context) (discovery.Result, error)) {
	if !s.discovering.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "a discovery run is already in progress")
		return
	}
	defer s.discovering.Store(false)
	s.audit(r, sess.UserName, action, detail)
	ctx, cancel := context.WithTimeout(s.Context, discoveryTimeout)
	defer cancel()
	res, err := run(ctx)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) dismissSuggestion(w http.ResponseWriter, r *http.Request, sess store.Session) {
	sug, err := s.discovery.Dismiss(r.Context(), r.PathValue("id"))
	if errors.Is(err, discovery.ErrNoSuggestion) {
		writeError(w, http.StatusGone, errSuggestionGone)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.audit(r, sess.UserName, "dismissed suggestion", sug.Type+" "+sug.URL)
	w.WriteHeader(http.StatusNoContent)
}

const errSuggestionGone = "this suggestion has expired; run discovery again"
