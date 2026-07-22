//go:build darwin || linux

package platform

import (
	"fmt"
	"os"
)

// OpenPrivateFile creates or opens path with owner-only permission applied at
// creation time (mode 0600 on the creating open).
func OpenPrivateFile(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag, 0o600)
}

// WritePrivateFile writes data to a file that is owner-only from creation.
func WritePrivateFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

// MkdirPrivate creates dir (and missing parents) owner-only. An existing
// directory must itself verify as owner-only — a pre-existing permissive
// directory is never silently reused (R1-CX-F3).
func MkdirPrivate(dir string) error {
	if _, err := os.Lstat(dir); err == nil {
		return VerifyPrivateDir(dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return VerifyPrivateDir(dir)
}

// MkdirTempPrivate creates an owner-only temporary directory with the given
// prefix under the system temp root.
func MkdirTempPrivate(prefix string) (string, error) {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// VerifyPrivateFile fails closed unless the existing file grants no
// group/other access. The error text carries the observed permission so call
// sites can wrap it with their artifact identity.
func VerifyPrivateFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("permission %o exposes group/other", st.Mode().Perm())
	}
	return nil
}

// VerifyPrivateDir fails closed unless path is a non-symlink directory that
// grants no group/other access.
func VerifyPrivateDir(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("private directory unavailable: fail-closed: %w", err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() || st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("directory %s is not an owner-only directory: fail-closed", path)
	}
	return nil
}
