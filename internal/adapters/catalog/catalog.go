// Package catalog is the single place that lists every adapter Observarr ships with. Nothing
// outside internal/adapters/... names an app type.
package catalog

import (
	"observarr/internal/adapter"
	"observarr/internal/adapters/arr"
	"observarr/internal/adapters/crowdsec"
	"observarr/internal/adapters/jellyfin"
	"observarr/internal/adapters/rclone"
)

// Register adds every known adapter to r. Add new adapters here; this list is the whole catalog.
func Register(r *adapter.Registry) {
	for _, d := range arr.Definitions {
		r.Register(arr.New(d))
	}
	r.Register(crowdsec.New())
	r.Register(rclone.New())
	r.Register(jellyfin.New())
}
