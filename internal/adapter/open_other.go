//go:build !unix

package adapter

// noFollowNonBlock is empty outside Unix: Observarr runs on Linux, this only keeps other dev builds compiling.
const noFollowNonBlock = 0
