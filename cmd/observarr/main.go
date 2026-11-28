// Command observarr runs the Observarr server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"observarr/internal/adapter"
	"observarr/internal/adapters/catalog"
	"observarr/internal/alert"
	"observarr/internal/api"
	"observarr/internal/auth"
	"observarr/internal/collect/docker"
	"observarr/internal/collect/local"
	"observarr/internal/config"
	"observarr/internal/poller"
	"observarr/internal/secret"
	"observarr/internal/shape"
	"observarr/internal/state"
	"observarr/internal/store"
	"observarr/internal/system"
	"observarr/web"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

// errInUse means another process holds the data directory's lock.
var errInUse = errors.New("locked by another process")

// samplerStopWait bounds how long shutdown waits for the system sampler.
const samplerStopWait = 2 * time.Second

func main() {
	restrictFileModes()
	health := flag.Bool("health", false, "check the running server's health endpoint and exit (Docker HEALTHCHECK)")
	flag.Parse()

	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *health {
		os.Exit(checkHealth(cfg.Listen))
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(cfg, log); err != nil {
		log.Error("observarr stopped", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	unlock, err := prepareDataDir(cfg.DataDir)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := openStore(ctx, filepath.Join(cfg.DataDir, "observarr.db"), cfg.ResetDB, log)
	if err != nil {
		return err
	}
	defer st.Close()
	go upkeep(ctx, st, log)

	box, err := openBox(ctx, cfg, st, log)
	if err != nil {
		return err
	}
	setupToken, setupTokenFile, err := prepareSetup(ctx, st, cfg.DataDir, log)
	if err != nil {
		return err
	}
	if cfg.SecureCookies && len(cfg.TrustedProxies) == 0 {
		log.Warn("login needs HTTPS, but Observarr serves plain HTTP and no trusted proxy is set: behind an HTTPS " +
			"reverse proxy set OBSERVARR_TRUSTED_PROXIES to the proxy's address; on a plain-HTTP network set " +
			"OBSERVARR_INSECURE_COOKIES=1")
	}
	limiter, err := auth.OpenLimiter(ctx, st, time.Now)
	if err != nil {
		return fmt.Errorf("load known login addresses: %w", err)
	}

	reg := adapter.NewRegistry()
	catalog.Register(reg)
	hub := state.NewHub()
	httpc := adapter.NewHTTPClient()
	// The full list of decisions an intrusion prevention system enforces can be long, and media requests come in
	// slowly: once a minute is enough for both.
	pl := poller.New(poller.Config{Registry: reg, Hub: hub, HTTP: httpc, Log: log,
		Intervals: map[shape.Kind]time.Duration{shape.KindSecurity: time.Minute, shape.KindRequests: time.Minute}})
	defer pl.Close() // deferred after st.Close, so it runs first: no probe outlives the store
	coll, err := collector(cfg, log)
	if err != nil {
		return err
	}
	sampler := system.New(coll, hub.Watchers, log)
	sampler.KeepTraffic(st, system.NewTraffic(time.Local, cfg.TrafficBudget.Period, cfg.TrafficBudget.Limit))
	sampled := make(chan struct{})
	go func() { sampler.Run(ctx); close(sampled) }()
	defer func() { // ctx ends with stop, and with it the sampler; one stuck in a host call is not waited for
		stop()
		select {
		case <-sampled:
		case <-time.After(samplerStopWait):
			log.Warn("system sampler did not stop in time")
		}
	}()

	alerts := alert.New(alert.Config{Store: st, Box: box, Hub: hub, System: sampler.Snapshot,
		Security: func() alert.SecurityState { return securityState(limiter) }, Deps: alert.DefaultDeps(), Log: log})
	if err := alerts.Start(ctx); err != nil {
		return fmt.Errorf("start alerts: %w", err)
	}
	defer func() { stop(); alerts.Wait() }() // runs before st.Close: no delivery outlives the store

	server := &api.Server{
		Log:            log,
		Store:          st,
		Sessions:       &auth.Sessions{Store: st, Secure: cfg.SecureCookies, Now: time.Now},
		Limiter:        limiter,
		Config:         cfg,
		UI:             web.Dist(),
		Version:        version,
		Registry:       reg,
		Box:            box,
		Hub:            hub,
		Poller:         pl,
		System:         sampler,
		Alerts:         alerts,
		HTTP:           httpc,
		Context:        ctx,
		SetupToken:     setupToken,
		SetupTokenFile: setupTokenFile,
	}
	if err := server.StartServices(); err != nil {
		return fmt.Errorf("start services: %w", err)
	}
	// Every request's context ends right before Shutdown: live event streams never finish on their own, and
	// Shutdown would otherwise wait for them until its timeout.
	requests, endRequests := context.WithCancel(context.Background())
	defer endRequests()
	srv := newHTTPServer(requests, server.Handler())

	// Bind before announcing startup: a busy port must fail here, not inside the goroutine below where
	// "observarr started" would already have been logged.
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Listen, err)
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("observarr started", "listen", cfg.Listen, "version", version, "secure_cookies", cfg.SecureCookies,
		"trusted_proxies", len(cfg.TrustedProxies), "proxy_auth", cfg.ProxyAuth)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	stop() // undo the signal notification: a second Ctrl-C/SIGTERM now kills the process immediately instead of waiting out the graceful shutdown below
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	endRequests()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newHTTPServer returns the server for h; every request's context derives from base.
func newHTTPServer(base context.Context, h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		BaseContext:       func(net.Listener) context.Context { return base },
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    32 << 10, // net/http's default is 1 MiB
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: live updates (Server-Sent Events) keep responses open by design.
	}
}

// prepareDataDir creates the data folder, narrows it to its owner when this process owns it, checks that it
// is writable and locks it, so a second Observarr on the same folder stops instead of sharing the database.
// The returned function releases the lock.
func prepareDataDir(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err // the path is named below
		}
		return nil, fmt.Errorf("data directory %s cannot be created (%w) — create it as a folder writable by uid %d, "+
			"or set OBSERVARR_DATA", dir, err, os.Getuid())
	}
	narrowOwnDir(dir)
	if err := checkWritable(dir); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "observarr.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("data directory: %w", err)
	}
	if err := lockFile(lock); err != nil {
		lock.Close()
		if errors.Is(err, errInUse) {
			return nil, fmt.Errorf("data directory %s is in use by another Observarr: stop that one first", dir)
		}
		return nil, fmt.Errorf("lock data directory %s: %w", dir, err)
	}
	return func() { lock.Close() }, nil
}

// checkWritable fails with what to do when this process cannot create files in dir.
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return fmt.Errorf("data directory %s is not writable by uid %d — e.g. chown %d <host folder>",
			dir, os.Getuid(), os.Getuid())
	}
	probe.Close()
	return os.Remove(probe.Name())
}

// openStore opens the database and checks that it takes writes. A corrupt database is moved aside only on
// request (resetDB); otherwise starting fails with what to do.
func openStore(ctx context.Context, path string, resetDB bool, log *slog.Logger) (*store.Store, error) {
	st, err := store.Open(ctx, path, log)
	switch {
	case errors.Is(err, store.ErrCorrupt) && resetDB:
		moved, err := store.MoveAside(path)
		if err != nil {
			return nil, fmt.Errorf("move the corrupt database aside: %w", err)
		}
		log.Warn("moved the corrupt database aside and started with an empty one; remove OBSERVARR_RESET_DB",
			"moved_to", moved)
		if st, err = store.Open(ctx, path, log); err != nil {
			return nil, describeWriteError(path, err)
		}
	case errors.Is(err, store.ErrCorrupt):
		return nil, fmt.Errorf("%w — restore a backup (see %s), or start once with OBSERVARR_RESET_DB=1 to move it "+
			"aside and start empty (setup then needs the new setup token)", err, filepath.Join(filepath.Dir(path), "backups"))
	case err != nil:
		return nil, describeWriteError(path, err)
	case resetDB:
		log.Warn("OBSERVARR_RESET_DB=1 is set, but the database is fine: nothing was reset; remove the setting")
	}
	if err := st.Probe(ctx); err != nil {
		st.Close()
		return nil, describeWriteError(path, err)
	}
	return st, nil
}

// describeWriteError explains an error that means the database takes no writes; other errors pass unchanged.
func describeWriteError(path string, err error) error {
	if reason := store.Degraded(err); reason != "" {
		return fmt.Errorf("%s: %s — Observarr must be able to write it: check the data folder's owner, "+
			"permissions and free space (%w)", path, reason, err)
	}
	return err
}

// openBox loads the key that seals saved API keys, creating it on the first start. If the key is missing while
// sealed keys are saved, those can never be opened again: starting fails with what to do, unless
// OBSERVARR_RESET_KEYS=1 asks to drop them so they can be entered again.
func openBox(ctx context.Context, cfg config.Config, st *store.Store, log *slog.Logger) (*secret.Box, error) {
	path := cfg.SecretKeyFile
	_, statErr := os.Stat(path)
	missing := errors.Is(statErr, fs.ErrNotExist)
	switch sealed, err := st.HasSealedKeys(ctx); {
	case err != nil:
		return nil, err
	case missing && sealed && !cfg.ResetKeys:
		return nil, fmt.Errorf("credential key %s is missing, but saved API keys need it: restore the key file "+
			"from a backup, or start once with OBSERVARR_RESET_KEYS=1 to drop the saved API keys and enter them again", path)
	case missing && sealed:
		n, err := st.ClearSealedKeys(ctx)
		if err != nil {
			return nil, err
		}
		log.Warn("credential key missing: dropped the saved API keys and alert channel secrets; enter them again, "+
			"then remove OBSERVARR_RESET_KEYS", "dropped", n)
	case cfg.ResetKeys:
		log.Warn("OBSERVARR_RESET_KEYS=1 is set, but no saved API key needed dropping; remove the setting")
	}
	box, err := secret.LoadOrCreate(path)
	if err != nil {
		return nil, err
	}
	if missing {
		log.Warn("created credential key " + path + " — back it up; without it saved API keys cannot be decrypted")
	}
	return box, nil
}

// prepareSetup returns the setup token and the file holding it while no account exists. Each start before
// setup makes a new token; once setup is done, a token file left behind is removed.
func prepareSetup(ctx context.Context, st *store.Store, dataDir string, log *slog.Logger) (token, file string, err error) {
	file = filepath.Join(dataDir, "setup-token")
	hasUsers, err := st.HasUsers(ctx)
	if err != nil {
		return "", "", err
	}
	if hasUsers {
		if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", "", err
		}
		return "", "", nil
	}
	if token, err = auth.NewSetupToken(file); err != nil {
		return "", "", fmt.Errorf("write setup token: %w", err)
	}
	log.Warn("setup token: " + token + " (also in " + file + ")")
	return token, file, nil
}

// collector reads the configured host and Docker sources.
func collector(cfg config.Config, log *slog.Logger) (*local.Local, error) {
	lc := local.Config{ProcRoot: cfg.HostProc, CgroupRoot: cfg.HostCgroup, Disks: cfg.HostDisks}
	if cfg.DockerHost != "" {
		dc, err := docker.New(cfg.DockerHost)
		if err != nil {
			return nil, err
		}
		lc.Docker = dc
		if strings.HasPrefix(cfg.DockerHost, "unix://") {
			log.Warn("Docker socket mounted directly: Observarr only reads, but the socket grants root access — " +
				"prefer a socket proxy")
		}
	}
	return local.New(lc), nil
}

// upkeep removes expired sessions, old audit rows, login addresses older than auth.KnownFor, old daily
// traffic totals, old history buckets, old alert log entries and ended mutes, now and then hourly, until ctx
// ends.
func upkeep(ctx context.Context, st *store.Store, log *slog.Logger) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for now := time.Now(); ; {
		if _, err := st.DeleteExpiredSessions(ctx, now); err != nil && ctx.Err() == nil {
			log.Warn("removing expired sessions failed", "err", err)
		}
		if err := st.PruneAudit(ctx, now); err != nil && ctx.Err() == nil {
			log.Warn("removing old audit rows failed", "err", err)
		}
		if err := st.PruneKnownLogins(ctx, now.Add(-auth.KnownFor)); err != nil && ctx.Err() == nil {
			log.Warn("removing old login addresses failed", "err", err)
		}
		if err := st.PruneTraffic(ctx, now); err != nil && ctx.Err() == nil {
			log.Warn("removing old traffic totals failed", "err", err)
		}
		if err := st.PruneHistory(ctx, now); err != nil && ctx.Err() == nil {
			log.Warn("removing old history failed", "err", err)
		}
		if err := st.PruneAlertLog(ctx); err != nil && ctx.Err() == nil {
			log.Warn("removing old alert log entries failed", "err", err)
		}
		if err := st.PruneAlertMutes(ctx, now); err != nil && ctx.Err() == nil {
			log.Warn("removing ended mutes failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case now = <-tick.C:
		}
	}
}

// securityState is what alerts read of the sign-in limiter: the addresses locked out, the failed attempts in the
// lockout window and how long the server-wide limit still holds.
func securityState(l *auth.Limiter) alert.SecurityState {
	addrs, serverLocked := l.Status()
	st := alert.SecurityState{ServerLocked: serverLocked}
	for _, a := range addrs {
		st.Failed += a.Attempts
		if a.Locked > 0 {
			st.LockedOut++
		}
	}
	return st
}

// checkHealth asks the running server for /api/health and returns the process exit code.
func checkHealth(listen string) int {
	url, err := healthURL(listen)
	if err != nil {
		return 1
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// healthURL turns a listen address (OBSERVARR_LISTEN, e.g. "host:port", ":port" or "[::1]:port") into the
// URL to probe. A wildcard host (empty, "0.0.0.0" or "::") is probed on 127.0.0.1, since the server also
// accepts connections there; a specific host is probed on that same host.
func healthURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("listen address %q: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/health", nil
}
