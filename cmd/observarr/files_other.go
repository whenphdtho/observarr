//go:build !unix

package main

import "os"

// Observarr runs on Linux; elsewhere these only keep the build working.

func restrictFileModes()      {}
func narrowOwnDir(string)     {}
func lockFile(*os.File) error { return nil }
