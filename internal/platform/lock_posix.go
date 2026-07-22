//go:build darwin || linux

package platform

import (
	"fmt"
	"os"
	"syscall"
)

// LockExclusive takes an exclusive advisory flock on the open sidecar file,
// blocking until acquired. Cooperating acrelay processes serialize through
// it; a writer that ignores the lock is stopped by the store CAS/revision
// guard, never by this lock alone.
func LockExclusive(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("exclusive lock failed: fail-closed, refusing unserialized mutation: %w", err)
	}
	return nil
}

// Unlock releases the exclusive lock. Closing the file also releases it;
// abrupt process termination releases flock locks kernel-side.
func Unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
