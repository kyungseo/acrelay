//go:build windows

package platform

import "os"

// InterruptSignals lists the parent-termination signals the CLI's two-stage
// dispatch policy consumes on Windows. Only Ctrl+C/Ctrl+Break style console
// interrupts are deliverable (os.Interrupt); there is no SIGTERM delivery.
// An external TerminateProcess of acrelay runs no handler at all — the
// durable dispatch journal remains the authoritative recovery path, exactly
// as for POSIX SIGKILL. This is a recorded platform difference, not an
// equivalence claim.
func InterruptSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
