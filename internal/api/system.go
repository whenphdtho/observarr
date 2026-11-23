package api

import (
	"net/http"

	"observarr/internal/store"
	"observarr/internal/system"
)

// Sampler is what the API reads of the system sampler (*system.Sampler).
type Sampler interface {
	Snapshot() system.Snapshot
	History() []system.HostView
	Network(rng string) system.TrafficView
	Subscribe() (<-chan system.Snapshot, func())
}

func (s *Server) systemState(w http.ResponseWriter, r *http.Request, _ store.Session) {
	writeBulkJSON(w, r, http.StatusOK, s.systemSnapshot())
}

func (s *Server) systemHistory(w http.ResponseWriter, r *http.Request, _ store.Session) {
	h := []system.HostView{}
	if s.System != nil {
		h = s.System.History()
	}
	writeBulkJSON(w, r, http.StatusOK, h)
}

// network returns the traffic over the last five minutes (?range=5m, the default) or the last day (?range=24h),
// with the service linked to each container.
func (s *Server) network(w http.ResponseWriter, r *http.Request, _ store.Session) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "5m"
	}
	if rng != "5m" && rng != "24h" {
		writeError(w, http.StatusBadRequest, `range: "5m" or "24h"`)
		return
	}
	v := system.TrafficView{Range: rng, Times: []int64{}, Containers: []system.ContainerTraffic{}}
	if s.System != nil {
		v = s.System.Network(rng)
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
