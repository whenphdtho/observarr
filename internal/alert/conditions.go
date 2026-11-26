package alert

import (
	"cmp"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"
	"time"

	"observarr/internal/shape"
	"observarr/internal/state"
	"observarr/internal/system"
)

// Condition is one thing that needs attention now. Key identifies it across evaluations: the same problem keeps
// its key while it lasts, so it is reported once. Scope names the data it was read from: a condition is only
// considered over when its scope was read again without it (see Input.known).
type Condition struct {
	Key      string   `json:"key"`
	Rule     Rule     `json:"rule"`
	Severity Severity `json:"severity"`
	Scope    string   `json:"scope"`
	Service  int64    `json:"service,omitempty"` // the service it is about; 0 for none
	Name     string   `json:"name"`              // what it is about, e.g. the service's name; "" for none
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	View     string   `json:"view"` // the UI view showing the details, e.g. "#/apps"
}

// Input is everything conditions are read from.
type Input struct {
	Now      time.Time
	Services []state.ServiceState
	// System is the latest host and container sample; Host nil means the host could not be read.
	System system.Snapshot
	// Security is the sign-in limiter's state.
	Security SecurityState
}

// SecurityState is what the sign-in limiter holds now.
type SecurityState struct {
	LockedOut    int           // addresses locked out
	Failed       int           // failed sign-ins within the lockout window, of every address
	ServerLocked time.Duration // how long the server-wide limit still refuses new addresses
}

// Evaluate lists every condition of the enabled rules, and the scopes that were read. It mirrors the "Needs
// attention" list of the UI (attention in web/src/lib/status.js and mountAttention in storage.js) and adds what
// only alerts need: thresholds, traffic and sign-ins. The UI keeps its own list, worked out from the live data it
// already holds the moment it changes and filtered by its search; alerts wait out blips first (see Rules.debounce).
func Evaluate(in Input, r Rules) (conds []Condition, known map[string]bool) {
	e := evaluation{rules: r, known: map[string]bool{}}
	for i := range in.Services {
		e.service(&in.Services[i])
	}
	if !in.System.At.IsZero() { // else the host has not been sampled yet
		e.containers(in.System)
		e.disks(in.System.Host)
		e.traffic(in.System.Traffic, in.Now)
	}
	e.security(in.Security)
	return e.out, e.known
}

type evaluation struct {
	rules Rules
	out   []Condition
	known map[string]bool
}

func (e *evaluation) add(c Condition) {
	if e.rules.on(c.Rule) {
		e.out = append(e.out, c)
	}
}

// scope names one reading of one service, e.g. "7/health".
func scope(id int64, part string) string { return strconv.FormatInt(id, 10) + "/" + part }

// digest shortens free text for a key, so a key stays small whatever an app reports.
func digest(parts ...string) string {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return strconv.FormatUint(h.Sum64(), 36)
}

func (e *evaluation) service(svc *state.ServiceState) {
	health, ok := svc.Readings[shape.KindHealth]
	if !ok {
		return // not checked yet: nothing is known
	}
	id := svc.ID
	e.known[scope(id, "down")] = true
	if !svc.Up {
		// Down: whatever else it reported before is not known now, so none of it is over either.
		e.add(Condition{Key: "down/" + strconv.FormatInt(id, 10), Rule: RuleDown, Severity: Critical,
			Scope: scope(id, "down"), Service: id, Name: svc.Name, Title: svc.Name + " is down",
			Message: health.Error, View: "#/apps"})
		return
	}

	var warned []string // the sources of its warnings, to leave out mount problems it reports itself
	if h, ok := health.Data.(shape.Health); ok {
		e.known[scope(id, "health")] = true
		view := "#/apps"
		if slices.Contains(svc.Kinds, shape.KindMounts) {
			view = "#/storage"
		} else if slices.Contains(svc.Kinds, shape.KindSecurity) {
			view = "#/security"
		}
		for _, w := range h.Warnings {
			if w.Source != "" {
				warned = append(warned, strings.TrimSuffix(w.Source, ":"))
			}
			if w.Level == shape.LevelInfo {
				continue
			}
			sev := Warning
			if w.Level == shape.LevelError {
				sev = Critical
			}
			msg := w.Message
			if w.Source != "" {
				msg = w.Source + ": " + w.Message
			}
			e.add(Condition{Key: "health/" + strconv.FormatInt(id, 10) + "/" + digest(w.Source, w.Message),
				Rule: RuleHealth, Severity: sev, Scope: scope(id, "health"), Service: id, Name: svc.Name,
				Title: svc.Name + " reports a problem", Message: msg, View: view})
		}
	}

	if q, ok := reading[shape.Queue](svc, shape.KindQueue); ok {
		e.known[scope(id, "queue")] = true
		for _, it := range q.Items {
			if it.Problem == "" && it.Status != "failed" && it.Status != "warning" {
				continue
			}
			e.add(Condition{Key: "download/" + strconv.FormatInt(id, 10) + "/" + digest(it.Title),
				Rule: RuleDownloads, Severity: Warning, Scope: scope(id, "queue"), Service: id, Name: svc.Name,
				Title: svc.Name + ": a download is stuck", Message: it.Title + ": " + cmp.Or(it.Problem, it.Status),
				View: "#/apps"})
		}
	}

	if m, ok := reading[shape.Mounts](svc, shape.KindMounts); ok {
		e.known[scope(id, "mounts")] = true
		for _, mt := range m.Mounts {
			e.mount(svc, mt, warned)
		}
	}

	if rq, ok := reading[shape.Requests](svc, shape.KindRequests); ok {
		e.known[scope(id, "requests")] = true
		if n := rq.Counts.Pending; n > 0 {
			e.add(Condition{Key: "requests/" + strconv.FormatInt(id, 10), Rule: RuleRequests, Severity: Info,
				Scope: scope(id, "requests"), Service: id, Name: svc.Name, Title: svc.Name + ": requests waiting",
				Message: plural(n, "request waits", "requests wait") + " for approval", View: "#/apps"})
		}
	}
}

// reading returns svc's latest reading of kind when it holds data of type T and no error.
func reading[T any](svc *state.ServiceState, kind shape.Kind) (T, bool) {
	r, ok := svc.Readings[kind]
	if !ok || r.Error != "" {
		var zero T
		return zero, false
	}
	v, ok := r.Data.(T)
	return v, ok
}

// mount adds what is wrong with one mount, as the Storage view's mountProblem says it, and a nearly full cache.
// A problem the service reports as a health warning about that mount is left out: it is alerted on already.
func (e *evaluation) mount(svc *state.ServiceState, m shape.Mount, warned []string) {
	if slices.Contains(warned, strings.TrimSuffix(m.Remote, ":")) {
		return
	}
	var problem string
	switch {
	case m.State == shape.MountUnmounted:
		problem = "not mounted"
	case m.OutOfSpace:
		problem = "cache out of space"
	case m.ErroredFiles > 0:
		problem = plural(int(m.ErroredFiles), "file", "files") + " failed to upload"
	}
	key := strconv.FormatInt(svc.ID, 10) + "/" + digest(m.Remote)
	if problem != "" {
		e.add(Condition{Key: "mount/" + key, Rule: RuleStorage, Severity: Critical, Scope: scope(svc.ID, "mounts"),
			Service: svc.ID, Name: svc.Name, Title: svc.Name + ": " + m.Remote + " needs attention", Message: problem,
			View: "#/storage"})
		return
	}
	if m.Cache && m.CacheMaxBytes > 0 {
		if pct := percent(m.CacheUsedBytes, m.CacheMaxBytes); pct >= e.rules.FullPercent {
			e.add(Condition{Key: "cache/" + key, Rule: RuleStorage, Severity: Warning, Scope: scope(svc.ID, "mounts"),
				Service: svc.ID, Name: svc.Name, Title: "Cache of " + m.Remote + " nearly full",
				Message: fmt.Sprintf("%d%% of its limit used", pct), View: "#/storage"})
		}
	}
}

// containers adds the containers that are unhealthy, restarting or dead, as the Overview lists them.
func (e *evaluation) containers(snap system.Snapshot) {
	if slices.Contains(snap.Notices, system.NoticeDocker) || slices.Contains(snap.Notices, system.NoticeDockerError) {
		return // not configured, or not read: nothing is known
	}
	e.known["containers"] = true
	for _, c := range snap.Containers {
		var sev Severity
		var what string
		switch {
		case c.Health == "unhealthy":
			sev, what = Critical, "unhealthy"
		case c.State == "restarting":
			sev, what = Warning, "restarting"
		case c.State == "dead":
			sev, what = Critical, "dead"
		default:
			continue
		}
		e.add(Condition{Key: "container/" + c.Name, Rule: RuleDown, Severity: sev, Scope: "containers", Name: c.Name,
			Title: "Container " + c.Name + " is " + what, Message: "Docker reports it " + what, View: "#/system"})
	}
}

// disks adds the host's file systems that are nearly full. Paths on one file system report the same figures; it
// is reported once, under its first path.
func (e *evaluation) disks(h *system.HostView) {
	if h == nil {
		return
	}
	e.known["host"] = true
	seen := map[[3]uint64]bool{}
	for _, d := range h.Disks {
		fig := [3]uint64{d.Total, d.Free, d.Avail}
		if seen[fig] || d.Total == 0 {
			continue
		}
		seen[fig] = true
		used := d.Total - min(d.Free, d.Total)
		pct := percent(int64(used), int64(used+d.Avail))
		if pct < e.rules.FullPercent {
			continue
		}
		sev := Warning
		if pct >= 98 {
			sev = Critical
		}
		e.add(Condition{Key: "disk/" + d.Path, Rule: RuleStorage, Severity: sev, Scope: "host", Name: d.Path,
			Title: "Disk " + d.Path + " nearly full", Message: fmt.Sprintf("%d%% used", pct), View: "#/storage"})
	}
}

// traffic adds the traffic budget at 80 % and at 100 %. Each is keyed by its period, so it is sent once per day
// or month, and ends quietly when the next one starts.
func (e *evaluation) traffic(t *system.TrafficSummary, now time.Time) {
	if t == nil || t.Budget == nil || t.Budget.Limit == 0 {
		return
	}
	e.known["traffic"] = true
	b := t.Budget
	period := now.Format("2006-01-02")
	if b.Period == "month" {
		period = now.Format("2006-01")
	}
	pct := percent(int64(min(b.Used, 1<<62)), int64(min(b.Limit, 1<<62)))
	for _, step := range []struct {
		at  int
		sev Severity
	}{{100, Critical}, {80, Warning}} {
		if pct < step.at {
			continue
		}
		e.add(Condition{Key: fmt.Sprintf("traffic/%d/%s", step.at, period), Rule: RuleTraffic, Severity: step.sev,
			Scope: "traffic", Title: fmt.Sprintf("Traffic budget %d%% used", step.at),
			Message: fmt.Sprintf("%d%% of this %s's budget is used", pct, b.Period), View: "#/network"})
	}
}

func (e *evaluation) security(s SecurityState) {
	e.known["security"] = true
	if s.LockedOut > 0 {
		e.add(Condition{Key: "security/lockout", Rule: RuleSecurity, Severity: Warning, Scope: "security",
			Title: "Sign-in lockout", Message: plural(s.LockedOut, "address is", "addresses are") +
				" locked out after too many failed sign-ins", View: "#/security"})
	}
	if s.ServerLocked > 0 {
		e.add(Condition{Key: "security/limited", Rule: RuleSecurity, Severity: Warning, Scope: "security",
			Title: "Sign-ins limited", Message: "Many addresses failed to sign in; for now only addresses that " +
				"signed in before may try", View: "#/security"})
	}
	if s.Failed >= e.rules.FailedSignIns {
		e.add(Condition{Key: "security/failed", Rule: RuleSecurity, Severity: Warning, Scope: "security",
			Title: "Many failed sign-ins", Message: plural(s.Failed, "failed sign-in", "failed sign-ins") +
				" in the last 15 minutes", View: "#/security"})
	}
}

// percent is part of whole as a whole percentage, rounded down, 0 when whole is 0.
func percent(part, whole int64) int {
	if whole <= 0 || part <= 0 {
		return 0
	}
	return int(float64(min(part, whole)) / float64(whole) * 100)
}

// plural is "1 thing" or "n things".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
