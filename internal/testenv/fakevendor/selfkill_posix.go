//go:build darwin || linux

package main

import "syscall"

// selfKill delivers SIGKILL to the fixture process itself — the POSIX
// signal-termination fixture used by the UNKNOWN classification tests.
func selfKill() {
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL)
	select {} // unreachable; SIGKILL is not handleable
}
