//go:build unix

package adapter

import "syscall"

const noFollowNonBlock = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
