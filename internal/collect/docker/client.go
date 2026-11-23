// Package docker is a minimal, read-only Docker Engine API client meant to sit behind a socket proxy.
// It only ever issues GET requests: there is no code path that could start, stop or change anything.
package docker

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"observarr/internal/collect"
)

const (
	maxAPI      = "1.52"           // newest Engine API whose shapes this client knows; newer daemons still serve it
	maxBody     = 8 << 20          // cap on any non-streaming response
	maxEvent    = 1 << 20          // cap on one events-stream line
	callTimeout = 10 * time.Second // per call, except the events stream
	inspects    = 8                // concurrent container inspects
	// MaxContainers bounds the containers listed.
	MaxContainers = 1000
)

// eventFilter asks for container lifecycle events only, not the exec_* events every healthcheck run produces.
const eventFilter = `{"type":["container"],"event":["create","start","restart","die","stop","kill","oom","pause","unpause","destroy","rename","health_status"]}`

var (
	// ErrInspect means containers were listed but none could be inspected, so PID, health and restart counts are
	// missing.
	ErrInspect = errors.New("docker: containers could not be inspected")
	// ErrTooMany means only the first MaxContainers containers were listed.
	ErrTooMany = errors.New("docker: more containers than are listed")
	// ErrDenied means the socket proxy refused an endpoint; the error names it.
	ErrDenied = errors.New("the socket proxy does not allow")
)

var (
	apiVersion  = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}$`)
	containerID = regexp.MustCompile(`^[0-9a-f]{64}$`) // only such IDs are kept, and put into a request path
)

// Client talks to one Docker Engine endpoint.
type Client struct {
	http *http.Client
	base string // scheme and host only; the path is always built here

	mu      sync.Mutex
	version string // negotiated API version, "" until the first successful /version

	// What inspecting each listed container found, kept while it stays in the same state and an events stream is
	// open (live): every event for a container drops its entry, and opening or losing the stream drops all. gen
	// counts the drops, so an inspection that overlapped one is not kept. Without a stream nothing is kept, as
	// nothing would tell of a change in health or a restart.
	imu       sync.Mutex
	inspected map[string]inspection
	gen       uint64
	live      bool
}

// inspection is what /containers/{id}/json added to a container in state.
type inspection struct {
	state        string
	pid          int
	restartCount int
	startedAt    time.Time
	health       string
}

// forget drops what was inspected of container id.
func (c *Client) forget(id string) {
	c.imu.Lock()
	defer c.imu.Unlock()
	c.gen++
	delete(c.inspected, id)
}

// setLive drops what was inspected of all containers and records whether an events stream is open.
func (c *Client) setLive(live bool) {
	c.imu.Lock()
	defer c.imu.Unlock()
	c.gen++
	clear(c.inspected)
	c.live = live
}

// ParseHost checks a Docker host address, "tcp://host:port" or "unix:///absolute/path", and returns the network and
// address to dial.
func ParseHost(host string) (network, address string, err error) {
	bad := fmt.Errorf("docker: %q is not tcp://host:port or unix:///path", host)
	u, err := url.Parse(host)
	if err != nil {
		return "", "", bad
	}
	switch u.Scheme {
	case "tcp":
		if u.Hostname() == "" || u.Port() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", "", bad
		}
		return "tcp", u.Host, nil
	case "unix":
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", "", bad
		}
		return "unix", u.Path, nil
	}
	return "", "", bad
}

// New returns a client for host (see ParseHost). It does not connect.
func New(host string) (*Client, error) {
	network, address, err := ParseHost(host)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &Client{
		base:      "http://docker",
		inspected: make(map[string]inspection),
		http: &http.Client{
			// No Client.Timeout: the events stream is long-lived. Other calls get callTimeout via their context.
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialer.DialContext(ctx, network, address) // always the configured endpoint, never a proxy
				},
				MaxIdleConnsPerHost:    inspects,
				IdleConnTimeout:        90 * time.Second,
				ResponseHeaderTimeout:  callTimeout,
				MaxResponseHeaderBytes: 64 << 10, // net/http's default is 10 MiB
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Containers lists all containers (the first MaxContainers, with an error wrapping ErrTooMany when there are more)
// and inspects the running ones for PID, restart count, start time and health; while an events stream is open, a
// container already inspected in its current state is not inspected again. A container whose inspect fails (e.g. it was just removed) keeps its
// list fields; if no running container could be inspected, the list comes with an error wrapping ErrInspect and
// the first failure.
func (c *Client) Containers(ctx context.Context) ([]collect.Container, error) {
	var list []struct {
		ID    string `json:"Id"`
		Names []string
		Image string
		State string
		Ports []struct {
			PrivatePort, PublicPort int
			Type                    string
		}
		Labels     map[string]string
		HostConfig struct{ NetworkMode string }
	}
	if err := c.getJSON(ctx, "/containers/json?all=1", &list); err != nil {
		return nil, err
	}
	var tooMany error
	out := make([]collect.Container, 0, min(len(list), MaxContainers))
	for _, l := range list {
		if !containerID.MatchString(l.ID) {
			continue // not a container ID Docker would give; it never goes into a request path or a cgroup lookup
		}
		if len(out) == MaxContainers {
			tooMany = ErrTooMany
			break
		}
		ct := collect.Container{ID: l.ID, Name: name(l.Names), Image: l.Image, State: l.State,
			NetworkMode: l.HostConfig.NetworkMode, Labels: labels(l.Labels)}
		for _, p := range l.Ports {
			port := collect.Port{Private: p.PrivatePort, Public: p.PublicPort, Proto: p.Type}
			if !slices.Contains(ct.Ports, port) { // one entry per IPv4 and IPv6 binding of the same port
				ct.Ports = append(ct.Ports, port)
			}
		}
		slices.SortFunc(ct.Ports, func(a, b collect.Port) int {
			if a.Private != b.Private {
				return a.Private - b.Private
			}
			if a.Public != b.Public {
				return a.Public - b.Public
			}
			return strings.Compare(a.Proto, b.Proto)
		})
		out = append(out, ct)
	}

	// Running containers not yet inspected in their current state.
	var todo []int
	known := 0
	c.imu.Lock()
	gen := c.gen
	for i := range out {
		if out[i].State != "running" {
			continue
		}
		if in, ok := c.inspected[out[i].ID]; ok && in.state == out[i].State {
			in.fill(&out[i])
			known++
		} else {
			todo = append(todo, i)
		}
	}
	c.imu.Unlock()

	found := make([]inspection, len(todo))
	errs := make([]error, len(todo))
	sem := make(chan struct{}, inspects)
	var wg sync.WaitGroup
	for j, i := range todo {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		wg.Go(func() {
			defer func() { <-sem }()
			found[j], errs[j] = c.inspect(ctx, out[i].ID)
			found[j].state = out[i].State
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var first error
	states := make(map[string]string, len(out))
	for i := range out {
		states[out[i].ID] = out[i].State
	}
	c.imu.Lock()
	maps.DeleteFunc(c.inspected, func(id string, in inspection) bool { return states[id] != in.state })
	for j, i := range todo {
		if errs[j] != nil {
			first = cmp.Or(first, errs[j])
			continue
		}
		found[j].fill(&out[i])
		known++
		if c.live && c.gen == gen { // no event since the list: what was inspected is still current
			c.inspected[out[i].ID] = found[j]
		}
	}
	c.imu.Unlock()
	if len(todo) > 0 && known == 0 {
		return out, errors.Join(tooMany, fmt.Errorf("%w: %w", ErrInspect, first))
	}
	return out, tooMany
}

// fill sets the fields of ct that only /containers/{id}/json has.
func (in inspection) fill(ct *collect.Container) {
	ct.PID, ct.RestartCount, ct.StartedAt, ct.Health = in.pid, in.restartCount, in.startedAt, in.health
}

// inspect reads what only /containers/{id}/json has.
func (c *Client) inspect(ctx context.Context, id string) (inspection, error) {
	var in struct {
		RestartCount int
		State        struct {
			Pid       int
			StartedAt string
			Health    *struct{ Status string }
		}
	}
	if err := c.getJSON(ctx, "/containers/"+id+"/json", &in); err != nil {
		return inspection{}, err
	}
	res := inspection{restartCount: in.RestartCount, pid: max(in.State.Pid, 0)}
	if t, err := time.Parse(time.RFC3339Nano, in.State.StartedAt); err == nil && t.Year() > 1 {
		res.startedAt = t
	}
	if h := in.State.Health; h != nil && h.Status != "none" {
		res.health = h.Status
	}
	return res, nil
}

// Events streams container events until ctx ends or the stream breaks, then closes the channel. Lines that are
// not container events, or not JSON, are skipped; a line over 1 MiB ends the stream.
func (c *Client) Events(ctx context.Context) (<-chan collect.Event, error) {
	resp, err := c.get(ctx, "/events?filters="+url.QueryEscape(eventFilter))
	if err != nil {
		return nil, err
	}
	c.setLive(true) // anything may have changed while no stream was open
	ch := make(chan collect.Event, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		defer c.setLive(false) // before the channel closes: from then on, nothing is kept
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 4096), maxEvent)
		for sc.Scan() {
			var m struct {
				Type, Action string
				Actor        struct {
					ID         string
					Attributes map[string]string
				}
				Time     int64 `json:"time"`
				TimeNano int64 `json:"timeNano"`
			}
			if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "container" || strings.HasPrefix(m.Action, "exec_") {
				continue // exec_* (e.g. every healthcheck run) is filtered out by the daemon already; this is a guard
			}
			action, _, _ := strings.Cut(m.Action, ":") // "health_status: healthy"
			ev := collect.Event{ID: m.Actor.ID, Name: m.Actor.Attributes["name"], Action: strings.TrimSpace(action), At: time.Unix(m.Time, 0)}
			if m.TimeNano > 0 {
				ev.At = time.Unix(0, m.TimeNano)
			}
			c.forget(ev.ID)
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// getJSON GETs path with callTimeout and decodes at most maxBody bytes of JSON into v.
func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decode(resp.Body, endpoint(path), v)
}

// get GETs the versioned path (negotiating the version first if needed) and returns a 200 response.
// It is the only place a request is built, and it only builds GETs.
func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	v, err := c.negotiate(ctx)
	if err != nil {
		return nil, err
	}
	return c.do(ctx, "/v"+v+path)
}

// negotiate returns the API version to speak: the daemon's, capped at maxAPI. A failure is not cached.
func (c *Client) negotiate(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version != "" {
		return c.version, nil
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := c.do(ctx, "/version")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var ver struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := decode(resp.Body, "/version", &ver); err != nil {
		return "", err
	}
	if !apiVersion.MatchString(ver.APIVersion) {
		return "", errors.New("docker: the daemon reported an unusable API version")
	}
	c.version = ver.APIVersion
	if older(maxAPI, c.version) {
		c.version = maxAPI
	}
	return c.version, nil
}

func (c *Client) do(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, fmt.Errorf("docker: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: GET %s: %w", endpoint(path), err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("docker: %w %s", ErrDenied, endpoint(path))
	}
	return nil, fmt.Errorf("docker: GET %s: status %d", endpoint(path), resp.StatusCode)
}

func decode(body io.Reader, what string, v any) error {
	b, err := io.ReadAll(io.LimitReader(body, maxBody+1))
	if err != nil {
		return fmt.Errorf("docker: read %s: %w", what, err)
	}
	if len(b) > maxBody {
		return fmt.Errorf("docker: response from %s exceeds %d bytes", what, maxBody)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("docker: %s: the response is not the expected JSON", what)
	}
	return nil
}

// endpoint is path without the version prefix, query and container ID, for messages ("/containers/{id}/json").
func endpoint(path string) string {
	path, _, _ = strings.Cut(path, "?")
	if rest, ok := strings.CutPrefix(path, "/v"); ok {
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			path = rest[i:]
		}
	}
	if id, ok := strings.CutPrefix(path, "/containers/"); ok && strings.HasSuffix(id, "/json") && id != "json" {
		return "/containers/{id}/json"
	}
	return path
}

// older reports whether API version a is older than b; both match apiVersion.
func older(a, b string) bool {
	amaj, amin, _ := strings.Cut(a, ".")
	bmaj, bmin, _ := strings.Cut(b, ".")
	x, _ := strconv.Atoi(amaj)
	y, _ := strconv.Atoi(bmaj)
	if x != y {
		return x < y
	}
	x, _ = strconv.Atoi(amin)
	y, _ = strconv.Atoi(bmin)
	return x < y
}

// name returns the container's own name: the one without a further "/" (others are legacy link aliases).
func name(names []string) string {
	for _, n := range names {
		if n = strings.TrimPrefix(n, "/"); !strings.Contains(n, "/") {
			return n
		}
	}
	if len(names) > 0 {
		return strings.TrimPrefix(names[0], "/")
	}
	return ""
}

// labels keeps only the labels Observarr reads.
func labels(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		if strings.HasPrefix(k, "observarr.") || strings.HasPrefix(k, "com.docker.compose.") {
			if out == nil {
				out = make(map[string]string)
			}
			out[k] = v
		}
	}
	return out
}
