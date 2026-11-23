// Package api serves Observarr's HTTP API and the embedded web UI.
package api

import (
	"context"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/auth"
	"observarr/internal/config"
	"observarr/internal/discovery"
	"observarr/internal/poller"
	"observarr/internal/secret"
	"observarr/internal/state"
	"observarr/internal/store"
)

// Server holds what the HTTP handlers need.
type Server struct {
	Log      *slog.Logger
	Store    *store.Store
	Sessions *auth.Sessions
	Limiter  *auth.Limiter
	Config   config.Config
	UI       fs.FS // built web app (index.html + assets/)
	Version  string

	Registry *adapter.Registry
	Box      *secret.Box // seals saved API keys
	Hub      *state.Hub
	Poller   *poller.Poller
	HTTP     *http.Client // the one client every adapter call uses
	System   Sampler      // host and container figures; nil leaves them empty
	// Context lives as long as the server. Poller loops run under it, never under a request's context.
	Context context.Context
	// SetupToken must accompany first-start setup; SetupTokenFile holds it and is removed once setup is done.
	SetupToken     string
	SetupTokenFile string

	hashSlots   chan struct{}     // bounds concurrent password hashing; see withHashSlot
	servicesMu  sync.Mutex        // serializes changes to services, so store, hub and poller always agree
	streams     atomic.Int32      // open live event streams; changed only under streamsMu
	pingEvery   time.Duration     // interval of keep-alive comments and session checks on live event streams
	systemEvery time.Duration     // least interval between "system" events on one live event stream
	discovery   *discovery.Engine // suggestions; keys found stay in here
	discovering atomic.Bool       // a discovery run is in progress

	streamsMu      sync.Mutex
	sessionStreams map[string]int // open live event streams per session, keyed by its CSRF token

	failMu   sync.Mutex
	failures map[failKey]failCount // the failures each group's latest audit row counts

	proxyMu     sync.Mutex
	proxyLogins map[string]time.Time // when each proxy-asserted user's login was last audited

	healthMu     sync.Mutex
	healthAt     time.Time // time of the latest write probe
	healthReason string    // why that probe failed; "" if it did not
}

// Handler returns the complete HTTP handler. Call it once, before serving.
func (s *Server) Handler() http.Handler {
	s.hashSlots = make(chan struct{}, runtime.NumCPU())
	if s.pingEvery == 0 {
		s.pingEvery = 25 * time.Second
	}
	if s.systemEvery == 0 {
		s.systemEvery = 2 * time.Second
	}
	s.discovery = discovery.New(s.Registry, s.HTTP, s.Store, time.Now, s.Log)
	if s.System != nil {
		go s.followContainers()
	}
	auth.DummyHash() // compute it now: built on first use, it would make the first unknown-user login slower
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("POST /api/setup", s.setup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.Handle("POST /api/logout", s.requireSession(s.logout))
	mux.Handle("GET /api/adapters", s.requireSession(s.adapters))
	mux.Handle("GET /api/services", s.requireSession(s.services))
	mux.Handle("POST /api/services/test", s.requireSession(s.testService))
	mux.Handle("POST /api/services", s.requireSession(s.createService))
	mux.Handle("PATCH /api/services/{id}", s.requireSession(s.updateService))
	mux.Handle("DELETE /api/services/{id}", s.requireSession(s.deleteService))
	mux.Handle("GET /api/events", s.requireSession(s.events))
	mux.Handle("GET /api/system", s.requireSession(s.systemState))
	mux.Handle("GET /api/system/history", s.requireSession(s.systemHistory))
	mux.Handle("GET /api/network", s.requireSession(s.network))
	mux.Handle("GET /api/security", s.requireSession(s.security))
	mux.Handle("POST /api/security/sign-out-others", s.requireSession(s.signOutOthers))
	mux.Handle("GET /api/discovery", s.requireSession(s.discoveryState))
	mux.Handle("POST /api/discovery/network", s.requireSession(s.discoverNetwork))
	mux.Handle("POST /api/discovery/linked", s.requireSession(s.discoverLinked))
	mux.Handle("POST /api/discovery/config", s.requireSession(s.discoverConfig))
	mux.Handle("POST /api/discovery/docker", s.requireSession(s.discoverDocker))
	mux.Handle("POST /api/discovery/{id}/dismiss", s.requireSession(s.dismissSuggestion))
	// Any other API request, including a wrong method on a known path, is a 404: simpler than 405 with Allow.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "unknown API endpoint")
	})
	// "/" without a method: "GET /" would overlap "/api/" with neither more specific, which ServeMux rejects.
	mux.Handle("/", s.ui())
	return recoverPanics(s.Log, securityHeaders(mux))
}

// healthEvery is the least interval between write probes; /api/health needs no login, so it must not be able
// to cause a write per request.
const healthEvery = 10 * time.Second

// health reports whether the server works, including whether its database takes writes.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if reason := s.probe(r.Context()); reason != "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded", "reason": reason})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.Version})
}

// probe returns why the database does not take writes, or "" if it does, from a probe at most healthEvery old.
func (s *Server) probe(ctx context.Context) string {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if time.Since(s.healthAt) < healthEvery {
		return s.healthReason
	}
	err := s.Store.Probe(ctx)
	if ctx.Err() != nil {
		return s.healthReason // the request ended; that says nothing about the database
	}
	s.healthAt, s.healthReason = time.Now(), store.Degraded(err)
	if err != nil && s.healthReason == "" {
		s.healthReason = "database write failed"
	}
	if err != nil {
		s.Log.Error("database write probe failed", "err", err)
	}
	return s.healthReason
}
