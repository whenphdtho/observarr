//go:build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

// restrictFileModes makes every file and folder Observarr creates private to its user.
func restrictFileModes() { syscall.Umask(0o077) }

// narrowOwnDir makes dir private (0700) when this process owns it; a folder owned by someone else is left as it
// is, since its owner chose its mode.
func narrowOwnDir(dir string) {
	fi, err := os.Stat(dir)
	if err != nil {
		return
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) == os.Getuid() && fi.Mode().Perm() != 0o700 {
		os.Chmod(dir, 0o700) // a failure shows in the writability check that follows, if it matters
	}
}

// lockFile takes an exclusive lock on f without waiting; it returns errInUse if another process holds it.
func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errInUse
	}
	return err
}
