//go:build windows

package adapter

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/kyungseo/acrelay/internal/testenv"
)

// Windows-only lifecycle fixtures (FEAT-20260722-002 R0-CX-F2): these run on
// the Windows CI/UTM lanes. They pin the pre-execution confinement invariant
// (suspended start → job assign → resume), immediate job termination on
// cancel (no graceful stage is claimed), descendant no-escape, and the
// no-orphan guarantee of KILL_ON_JOB_CLOSE when the job handle closes.

func processAlive(t *testing.T, pid int) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

func waitForFileWin(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("marker %s never appeared", path)
}

func installLifecycleFixture(t *testing.T, marker string, spawn bool) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lifecycle-child")
	fixture := "touch \"" + marker + "\"\n"
	if spawn {
		fixture += "spawn 60\n"
	}
	fixture += "sleep 60\n"
	testenv.InstallFakeVendor(t, bin, fixture)
	return bin + ".exe"
}

func readGrandchildPID(t *testing.T, exe string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(exe + ".grandchild"); err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil {
				t.Fatalf("grandchild pid file corrupt: %q", b)
			}
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("grandchild pid file never appeared")
	return 0
}

// Cancel terminates the whole job — direct child and its descendant —
// immediately (no graceful stage), and the tracker drains.
func TestWindowsJobCancelTerminatesDescendantTree(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	exe := installLifecycleFixture(t, marker, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newGroupCmd(ctx, 2*time.Second, exe)
	done := make(chan error, 1)
	go func() { done <- runWithProgress(cmd, nil) }()
	waitForFileWin(t, marker, 15*time.Second) // resume happened; user code ran
	grandchild := readGrandchildPID(t, exe)
	if !processAlive(t, grandchild) {
		t.Fatal("grandchild must be alive before cancellation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("job cancellation must reap the direct child promptly")
	}
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(t, grandchild) {
		if time.Now().After(deadline) {
			t.Fatal("descendant escaped the job: still alive after TerminateJobObject")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := ActiveProcessGroupCount(); got != 0 {
		t.Fatalf("tracker must drain after reap, got %d", got)
	}
}

// Closing the job handle (releaseGroup) with KILL_ON_JOB_CLOSE terminates
// the remaining tree — the no-orphan guarantee. The job handle was created
// after the child and is never inheritable, so only acrelay's close matters.
func TestWindowsJobKillOnCloseIsNoOrphanBackstop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	exe := installLifecycleFixture(t, marker, true)
	cmd := newGroupCmd(context.Background(), 2*time.Second, exe)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := trackGroup(cmd); err != nil {
		t.Fatalf("confinement must succeed: %v", err)
	}
	waitForFileWin(t, marker, 15*time.Second)
	grandchild := readGrandchildPID(t, exe)
	child := cmd.Process.Pid
	// N1: even on assertion failure the job handle must close so a failed CI
	// run does not hold the 60s children alive.
	t.Cleanup(func() { releaseGroup(cmd); _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	// Simulate the post-reap release path directly: close the job handle.
	releaseGroup(cmd)
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(t, child) || processAlive(t, grandchild) {
		if time.Now().After(deadline) {
			t.Fatalf("kill-on-close must terminate the tree (child alive=%v grandchild alive=%v)",
				processAlive(t, child), processAlive(t, grandchild))
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Wait()
}

// A cancel that races ahead of trackGroup kills the still-suspended child:
// the marker must never appear (no user code ran).
func TestWindowsCancelBeforeConfinementRunsNoUserCode(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	exe := installLifecycleFixture(t, marker, false)
	ctx, cancel := context.WithCancel(context.Background())
	cmd := newGroupCmd(ctx, 2*time.Second, exe)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()         // before trackGroup: child is still CREATE_SUSPENDED
	_ = cmd.Cancel() // exec.CommandContext would invoke this; call directly for determinism
	_ = cmd.Wait()
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("suspended child must not run user code when canceled before confinement")
	}
}

// --- R1-CX-F1 deterministic confinement-failure fixtures (seam-injected) ---

func withConfinementSeams(t *testing.T) {
	t.Helper()
	origSnap, origFirst, origNext := createToolhelpSnapshot, thread32First, thread32Next
	origOpen, origPid, origResume := openThread, getProcessIdOfThread, resumeThread
	t.Cleanup(func() {
		createToolhelpSnapshot, thread32First, thread32Next = origSnap, origFirst, origNext
		openThread, getProcessIdOfThread, resumeThread = origOpen, origPid, origResume
	})
}

// scriptThreads makes enumeration yield the given owner PIDs then end with
// the given terminal error (ERROR_NO_MORE_FILES = clean end).
func scriptThreads(owners []uint32, terminal error) {
	idx := 0
	step := func(entry *windows.ThreadEntry32) error {
		if idx < len(owners) {
			entry.OwnerProcessID = owners[idx]
			entry.ThreadID = uint32(1000 + idx)
			idx++
			return nil
		}
		return terminal
	}
	thread32First = func(_ windows.Handle, e *windows.ThreadEntry32) error { return step(e) }
	thread32Next = func(_ windows.Handle, e *windows.ThreadEntry32) error { return step(e) }
}

func startSuspendedFixture(t *testing.T) (*exec.Cmd, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "started")
	exe := installLifecycleFixture(t, marker, false)
	cmd := newGroupCmd(context.Background(), 2*time.Second, exe)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, marker
}

func assertConfinementFailure(t *testing.T, cmd *exec.Cmd, marker string, confineErr error, needle string) {
	t.Helper()
	if confineErr == nil || !strings.Contains(confineErr.Error(), needle) {
		t.Fatalf("confinement must fail closed with %q, got: %v", needle, confineErr)
	}
	if got := ActiveProcessGroupCount(); got != 0 {
		t.Fatalf("tracker must be rolled back, got %d entries", got)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(t, pid) {
		if time.Now().After(deadline) {
			t.Fatal("child must be terminated on confinement failure")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("child must never run user code on confinement failure")
	}
}

func TestWindowsConfinementFailuresFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(pid uint32)
		needle string
	}{
		{"zero threads", func(pid uint32) {
			scriptThreads(nil, windows.ERROR_NO_MORE_FILES)
		}, "has 0 threads"},
		{"multiple threads", func(pid uint32) {
			scriptThreads([]uint32{pid, pid}, windows.ERROR_NO_MORE_FILES)
		}, "has 2 threads"},
		{"enumeration error", func(pid uint32) {
			scriptThreads([]uint32{pid}, windows.ERROR_ACCESS_DENIED)
		}, "enumeration ended abnormally"},
		{"open thread failure", func(pid uint32) {
			openThread = func(uint32, bool, uint32) (windows.Handle, error) {
				return 0, windows.ERROR_ACCESS_DENIED
			}
		}, "open initial thread"},
		{"owner pid mismatch", func(pid uint32) {
			getProcessIdOfThread = func(windows.Handle) (uint32, error) { return pid + 1, nil }
		}, "TID reuse"},
		{"resume error", func(pid uint32) {
			resumeThread = func(windows.Handle) (uint32, error) { return 0, windows.ERROR_ACCESS_DENIED }
		}, "resume initial thread"},
		{"previous suspend count 0", func(pid uint32) {
			resumeThread = func(windows.Handle) (uint32, error) { return 0, nil }
		}, "previous suspend count 0"},
		{"previous suspend count 2", func(pid uint32) {
			resumeThread = func(windows.Handle) (uint32, error) { return 2, nil }
		}, "previous suspend count 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withConfinementSeams(t)
			cmd, marker := startSuspendedFixture(t)
			tc.mutate(uint32(cmd.Process.Pid))
			assertConfinementFailure(t, cmd, marker, trackGroup(cmd), tc.needle)
		})
	}
}

// The full runWithProgress wiring: a confinement failure surfaces as
// ConfinementError (started=false in adapters), and the "running" progress
// state is never emitted.
func TestWindowsConfinementFailureIsPreStartInResult(t *testing.T) {
	withConfinementSeams(t)
	marker := filepath.Join(t.TempDir(), "started")
	exe := installLifecycleFixture(t, marker, false)
	cmd := newGroupCmd(context.Background(), 2*time.Second, exe)
	seamSet := make(chan struct{})
	origHook := beforeTrackGroupHook
	beforeTrackGroupHook = func(c *exec.Cmd) {
		scriptThreads(nil, windows.ERROR_NO_MORE_FILES) // zero threads
		close(seamSet)
	}
	t.Cleanup(func() { beforeTrackGroupHook = origHook })
	var progressed []string
	err := runWithProgress(cmd, func(state, detail string) { progressed = append(progressed, state) })
	<-seamSet
	var confinement *ConfinementError
	if err == nil || !errors.As(err, &confinement) {
		t.Fatalf("runWithProgress must surface ConfinementError, got: %v", err)
	}
	if len(progressed) != 0 {
		t.Fatalf("no progress state may be emitted on confinement failure, got %v", progressed)
	}
	if startedForResult(cmd, err) {
		t.Fatal("confinement failure must classify started=false")
	}
}
