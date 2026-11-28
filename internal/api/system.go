package api

import (
	"cmp"
	"context"
	"net/http"

	"observarr/internal/store"
	"observarr/internal/system"
)

// Sampler is what the API reads of the system sampler (*system.Sampler).
type Sampler interface {
	Snapshot() system.Snapshot
	History() []system.HostView
	HostHistory(ctx context.Context, rng string) (system.HostSeries, error)
	Network(ctx context.Context, rng string) (system.TrafficView, error)
	Spark() system.Spark
	Subscribe() (<-chan system.Snapshot, func())
}

// errRange is the message for an unknown ?range=.
const errRange = `range: "5m", "24h", "7d", "30d" or "1y"`

// systemState returns the latest snapshot with the host's last hour by minute, for sparklines.
func (s *Server) systemState(w http.ResponseWriter, r *http.Request, _ store.Session) {
	v := struct {
		system.Snapshot
		system.Spark
	}{Snapshot: s.systemSnapshot()}
	if s.System != nil {
		v.Spark = s.System.Spark()
	} else {
		v.Spark.Spark = system.HostSeries{Range: "1h", Step: 60, Times: []int64{}, CPU: system.Values{},
			MemUsed: system.Values{}}
	}
	writeBulkJSON(w, r, http.StatusOK, v)
}

// systemHistory returns the host readings of the last five minutes without ?range=, and with it the host's
// CPU use and memory used over that range (see system.ValidRange) as series.
func (s *Server) systemHistory(w http.ResponseWriter, r *http.Request, _ store.Session) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		h := []system.HostView{}
		if s.System != nil {
			h = s.System.History()
		}
		writeBulkJSON(w, r, http.StatusOK, h)
		return
	}
	if !system.ValidRange(rng) {
		writeError(w, http.StatusBadRequest, errRange)
		return
	}
	v := system.HostSeries{Range: rng, Times: []int64{}, CPU: system.Values{}, MemUsed: system.Values{}}
	if s.System != nil {
		var err error
		if v, err = s.System.HostHistory(r.Context(), rng); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	writeBulkJSON(w, r, http.StatusOK, v)
}

// network returns the traffic over ?range= (see system.ValidRange; "5m" by default), with the service linked to
// each container.
func (s *Server) network(w http.ResponseWriter, r *http.Request, _ store.Session) {
	rng := cmp.Or(r.URL.Query().Get("range"), "5m")
	if !system.ValidRange(rng) {
		writeError(w, http.StatusBadRequest, errRange)
		return
	}
	v := system.TrafficView{Range: rng, Times: []int64{}, Containers: []system.ContainerTraffic{},
		Daily: []system.DayTotal{}}
	if s.System != nil {
		var err error
		if v, err = s.System.Network(r.Context(), rng); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	linked := map[string]string{} // container name → service name; the first by name when several share one
	for _, svc := range s.Hub.Snapshot() {
		if c := svc.Container; c != nil && linked[c.Name] == "" {
			linked[c.Name] = svc.Name
		}
	}
	for i := range v.Containers {
		v.Containers[i].Service = linked[v.Containers[i].Name]
	}
	writeBulkJSON(w, r, http.StatusOK, v)
}

// systemSnapshot is the sampler's latest snapshot; an empty one without a sampler.
func (s *Server) systemSnapshot() system.Snapshot {
	if s.System == nil {
		return system.Snapshot{Containers: []system.ContainerView{}, Notices: []string{}}
	}
	return s.System.Snapshot()
}
