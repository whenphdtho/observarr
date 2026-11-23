// Package catalog is the single place that lists every adapter Observarr ships with. Nothing
// outside internal/adapters/... names an app type.
package catalog

import (
	"observarr/internal/adapter"
	"observarr/internal/adapters/arr"
)

// Register adds every known adapter to r. Add new adapters here; this list is the whole catalog.
func Register(r *adapter.Registry) {
	for _, d := range arr.Definitions {
		r.Register(arr.New(d))
	}
}
