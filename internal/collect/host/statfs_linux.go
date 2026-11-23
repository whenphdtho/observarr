//go:build linux

package host

import (
	"syscall"

	"observarr/internal/collect"
)

// statfs measures path's file system.
func statfs(path string) (collect.Disk, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return collect.Disk{}, err
	}
	bs := uint64(st.Frsize) // block counts are in fragment-size units; Bsize is the preferred I/O size
	if bs == 0 {
		bs = uint64(st.Bsize)
	}
	return collect.Disk{Path: path, Total: st.Blocks * bs, Free: st.Bfree * bs, Avail: st.Bavail * bs}, nil
}
