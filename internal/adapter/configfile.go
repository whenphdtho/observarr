package adapter

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
)

// maxConfigBytes bounds a config file ReadConfigFile reads: an app's settings are a few kilobytes, so 1 MiB is
// generous while still refusing an unbounded file.
const maxConfigBytes = 1 << 20

// ReadConfigFile reads the file name from dir, a ConfigReader's folder, for an app's settings. A missing file is an
// error matching fs.ErrNotExist, which a reader takes as "not mine". The app can swap the file for anything at any
// moment, so what was opened is checked, not the name beforehand: a ConfigDir neither follows a symbolic link nor
// waits on a FIFO or device, and anything but a regular file of at most 1 MiB is refused. Errors name only name.
func ReadConfigFile(dir fs.FS, name string) ([]byte, error) {
	f, err := dir.Open(name)
	if err != nil {
		return nil, err // names the file already, e.g. "config.xml is a symbolic link, not read"
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New(name + " is not a regular file")
	}
	body, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if len(body) > maxConfigBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", name, maxConfigBytes)
	}
	return body, nil
}
