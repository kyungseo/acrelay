//go:build !windows

package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMkdirTempPrivateSystemAndDurableRootContracts(t *testing.T) {
	weakBase := filepath.Join(t.TempDir(), "system-temp")
	if err := os.Mkdir(weakBase, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(weakBase, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPrivateDir(weakBase); err == nil {
		t.Fatal("fixture system-temp parent must not satisfy the private durable-root contract")
	}
	t.Setenv("TMPDIR", weakBase)
	if got := filepath.Clean(os.TempDir()); got != filepath.Clean(weakBase) {
		t.Fatalf("system temp override not active: got %q want %q", got, weakBase)
	}

	systemChild, err := MkdirTempPrivate("acrelay-system-temp-")
	if err != nil {
		t.Fatalf("system-temp helper must accept a non-private system-owned parent: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(systemChild) })
	if err := VerifyPrivateDir(systemChild); err != nil {
		t.Fatalf("system-temp child must still be owner-only: %v", err)
	}

	if _, err := MkdirTempPrivateAt(weakBase, "acrelay-durable-"); err == nil {
		t.Fatal("durable-root helper must reject a pre-existing non-private base")
	}
	durableBase := filepath.Join(weakBase, "durable")
	durableChild, err := MkdirTempPrivateAt(durableBase, "acrelay-durable-")
	if err != nil {
		t.Fatalf("durable-root helper must create and verify its private base: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(durableBase) })
	if err := VerifyPrivateDir(durableChild); err != nil {
		t.Fatalf("durable child must be owner-only: %v", err)
	}
}
