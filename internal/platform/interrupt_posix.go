//go:build darwin || linux

package platform

import (
	"os"
	"syscall"
)

// InterruptSignals lists the parent-termination signals the CLI's two-stage
// dispatch policy consumes on this platform.
func InterruptSignals() []os.Signal {
	return []os.Signal{syscall.SIGINT, syscall.SIGTERM}
}
