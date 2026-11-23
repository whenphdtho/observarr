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

// systemSnapshot is the sampler's latest snapshot; an empty one without a sampler.
func (s *Server) systemSnapshot() system.Snapshot {
	if s.System == nil {
		return system.Snapshot{Containers: []system.ContainerView{}, Notices: []string{}}
	}
	return s.System.Snapshot()
}
