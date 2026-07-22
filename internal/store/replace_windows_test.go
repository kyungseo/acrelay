//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// R1-CX-F4: the Windows open-destination contract is pinned with an
// explicitly controlled sharing mode — a destination held open WITHOUT
// FILE_SHARE_DELETE must make the replace fail, preserve the prior bytes,
// and leave no temp — never skipped.
func TestAtomicReplaceOpenDestinationWindows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "canonical.md")
	if _, err := WritePrivateAtomic(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	// Read/write sharing only — deliberately no FILE_SHARE_DELETE.
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		uint32(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE), nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)

	if _, err := WritePrivateAtomic(path, []byte("second")); err == nil {
		t.Fatal("replace over a no-delete-sharing open destination must fail on Windows")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Fatalf("failed replace must preserve prior destination bytes, got %q", got)
	}
	if tmps := tempLeftovers(t, dir, "canonical.md"); len(tmps) != 0 {
		t.Fatalf("temp leftovers after failed replace: %v", tmps)
	}
	// After the holder closes, the same replace succeeds — the failure was
	// the sharing state, not a persistent condition (no hidden retry ran).
	windows.CloseHandle(h)
	if _, err := WritePrivateAtomic(path, []byte("second")); err != nil {
		t.Fatalf("replace after handle close must succeed: %v", err)
	}
}
