package adapter

import (
	"context"
	"io/fs"
)

// fakeDiscoverable implements Fingerprinter, Linker and ConfigReader so the compiler checks that
// all three optional interfaces can be satisfied together by one adapter.
type fakeDiscoverable struct{ fakeAdapter }

func (fakeDiscoverable) Fingerprint(ctx context.Context, t Target) (bool, string, error) {
	return false, "", nil
}

func (fakeDiscoverable) LinkedApps(ctx context.Context, t Target) ([]Candidate, error) {
	return nil, nil
}

func (fakeDiscoverable) ReadConfig(name string, dir fs.FS) (Candidate, bool, error) {
	return Candidate{}, false, nil
}

// These assignments are the compile-time test: they fail to build if fakeDiscoverable stops
// satisfying any of the three optional interfaces, or if their method signatures drift from this
// file.
var (
	_ Fingerprinter = fakeDiscoverable{}
	_ Linker        = fakeDiscoverable{}
	_ ConfigReader  = fakeDiscoverable{}
)
