package platform

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Cross-process exclusive-lock fixtures (FEAT-20260722-002 R0-CX-F5). Both
// the holder and the prober run as real subprocesses so a blocked or
// abandoned lock request always dies with its process — the parent never
// leaves a queued kernel lock request behind.

// TestLockHolderProcess is the subprocess holder: it takes the exclusive
// lock, drops a ".held" marker, and holds until killed (abrupt termination —
// Unlock never runs).
func TestLockHolderProcess(t *testing.T) {
	if os.Getenv("ACRELAY_PLATFORM_LOCK_HELPER") != "holder" {
		t.Skip("helper process mode only")
	}
	lockPath := os.Getenv("ACRELAY_PLATFORM_LOCK_PATH")
	f, err := OpenPrivateFile(lockPath, os.O_CREATE|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	if err := LockExclusive(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath+".held", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Second)
}

// TestLockProberProcess is the subprocess prober: it blocks on the exclusive
// lock and drops an ".acquired" marker once it gets it.
func TestLockProberProcess(t *testing.T) {
	if os.Getenv("ACRELAY_PLATFORM_LOCK_HELPER") != "prober" {
		t.Skip("helper process mode only")
	}
	lockPath := os.Getenv("ACRELAY_PLATFORM_LOCK_PATH")
	f, err := OpenPrivateFile(lockPath, os.O_CREATE|os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	if err := LockExclusive(f); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath+".acquired", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_ = Unlock(f)
	f.Close()
}

func startLockHelper(t *testing.T, mode, testName, lockPath string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", testName, "-test.v")
	cmd.Env = append(os.Environ(),
		"ACRELAY_PLATFORM_LOCK_HELPER="+mode,
		"ACRELAY_PLATFORM_LOCK_PATH="+lockPath)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func waitMarker(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// F5: while another process holds the exclusive lock, a prober process
// cannot acquire it; after the holder is killed abruptly (no Unlock), the OS
// releases the lock and a fresh prober acquires promptly.
func TestLockExclusiveContentionAndAbruptRelease(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "objective.lock")
	holder := startLockHelper(t, "holder", "TestLockHolderProcess", lockPath)
	defer func() {
		if holder.Process != nil {
			_ = holder.Process.Kill()
			_, _ = holder.Process.Wait()
		}
	}()
	if !waitMarker(lockPath+".held", 10*time.Second) {
		t.Fatal("holder subprocess never took the lock")
	}

	blockedProber := startLockHelper(t, "prober", "TestLockProberProcess", lockPath)
	if waitMarker(lockPath+".acquired", 700*time.Millisecond) {
		t.Fatal("exclusive lock must not be acquirable while another process holds it")
	}
	// The blocked prober's queued request must die with its process — never
	// linger to steal the release from the post-kill prober.
	_ = blockedProber.Process.Kill()
	_, _ = blockedProber.Process.Wait()

	// Abrupt holder termination: Unlock never ran; the OS releases the lock.
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = holder.Process.Wait()

	fresh := startLockHelper(t, "prober", "TestLockProberProcess", lockPath)
	defer func() { _ = fresh.Process.Kill(); _, _ = fresh.Process.Wait() }()
	if !waitMarker(lockPath+".acquired", 5*time.Second) {
		t.Fatal("exclusive lock must be released by the OS after abrupt holder termination")
	}
}
