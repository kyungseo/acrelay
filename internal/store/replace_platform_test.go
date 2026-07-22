package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tempLeftovers(t *testing.T, dir, base string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var tmps []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+base+".tmp-") {
			tmps = append(tmps, e.Name())
		}
	}
	return tmps
}

// F6 replace fixtures: the invariant is success-replaces / failure-preserves
// the prior destination bytes, with no temp leftovers and no hidden retry.
// Platform-different sharing semantics are asserted per GOOS, never
// equalized (FEAT-20260722-002 R0-CX-F6).
func TestAtomicReplaceFixtures(t *testing.T) {
	t.Run("fresh destination", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "canonical.md")
		if _, err := WritePrivateAtomic(path, []byte("first")); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "first" {
			t.Fatalf("fresh write mismatch: %q err=%v", got, err)
		}
		if tmps := tempLeftovers(t, dir, "canonical.md"); len(tmps) != 0 {
			t.Fatalf("temp leftovers after success: %v", tmps)
		}
	})

	t.Run("closed destination replaced", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "canonical.md")
		if _, err := WritePrivateAtomic(path, []byte("first")); err != nil {
			t.Fatal(err)
		}
		if _, err := WritePrivateAtomic(path, []byte("second")); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(path)
		if string(got) != "second" {
			t.Fatalf("closed destination must be replaced, got %q", got)
		}
		if tmps := tempLeftovers(t, dir, "canonical.md"); len(tmps) != 0 {
			t.Fatalf("temp leftovers after replace: %v", tmps)
		}
	})

	t.Run("open destination", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "canonical.md")
		if _, err := WritePrivateAtomic(path, []byte("first")); err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS == "windows" {
			// The Windows contract case (no-delete-sharing open destination →
			// failure + preservation) is pinned by the explicit-sharing
			// fixture in replace_windows_test.go — never skipped (R1-CX-F4).
			t.Skip("windows open-destination contract is pinned by TestAtomicReplaceOpenDestinationWindows")
		}
		held, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Close()
		_, replaceErr := WritePrivateAtomic(path, []byte("second"))
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		// POSIX rename replaces regardless of open handles; the held
		// descriptor keeps reading the old inode.
		if replaceErr != nil {
			t.Fatalf("POSIX replace over an open destination must succeed: %v", replaceErr)
		}
		if string(got) != "second" {
			t.Fatalf("POSIX replace result mismatch: %q", got)
		}
	})
}
