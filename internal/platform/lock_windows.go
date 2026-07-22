//go:build windows

package platform

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// lockRangeLen is the fixed non-zero byte range the exclusive lock covers.
// The claim is deliberately narrow: this is acrelay cooperative serialization
// over a fixed byte range of the sidecar file — LockFileEx byte-range locks
// are not semantically identical to POSIX flock (they can affect other file
// I/O on the locked range), so no flock equivalence is asserted. The sidecar
// is never read or written, so the range only ever serializes cooperating
// acrelay processes. A writer that ignores the lock is stopped by the store
// CAS/revision guard. Network/shared filesystems are unverified for this
// contract.
const lockRangeLen = 1

// LockExclusive takes an exclusive LockFileEx lock over the fixed byte range
// [0, lockRangeLen), blocking until acquired. Abrupt owner termination
// releases the lock when the handle is closed by the OS.
func LockExclusive(f *os.File) error {
	ol := new(windows.Overlapped) // offset 0; kept alive for the call only (synchronous handle)
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, lockRangeLen, 0, ol)
	if err != nil {
		return fmt.Errorf("exclusive lock failed: fail-closed, refusing unserialized mutation: %w", err)
	}
	return nil
}

// Unlock releases the fixed-range lock before the file is closed.
func Unlock(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, lockRangeLen, 0, ol)
}
