//go:build !linux

package host

import (
	"errors"

	"observarr/internal/collect"
)

// statfs measures nothing outside Linux: Observarr runs on Linux, this only keeps other dev builds compiling.
func statfs(string) (collect.Disk, error) { return collect.Disk{}, errors.ErrUnsupported }
