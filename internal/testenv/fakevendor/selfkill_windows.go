//go:build windows

package main

import (
	"fmt"
	"os"
)

// selfKill is a POSIX-only fixture construct: Windows has no signal-death
// wait status, and the corresponding UNKNOWN-classification fixtures are
// explicitly skipped there with a capability reason. Reaching this on
// Windows is a fixture bug, not a supported path.
func selfKill() {
	fmt.Fprintln(os.Stderr, "fakevendor: kill -KILL $$ is a POSIX-only fixture construct (skip this fixture on windows)")
	os.Exit(97)
}
