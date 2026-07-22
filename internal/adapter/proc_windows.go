//go:build windows

package adapter

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows child-tree lifecycle (FEAT-20260722-002 R0 contract, option B):
// the child is created CREATE_SUSPENDED, assigned to a fresh Job Object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, and only then resumed — the child never
// executes user code outside the job. os/exec closes the initial thread
// handle before Start returns, so the resume recovers it via a Toolhelp
// snapshot under strict fail-closed rules (R1-CX-F1): enumeration must end
// with ERROR_NO_MORE_FILES, exactly one thread may exist for the PID, the
// opened thread's owner PID is re-verified, and ResumeThread must report a
// previous suspend count of exactly 1. Any deviation terminates the
// never-resumed child, rolls back the tracker entry, closes every handle,
// and reports a pre-execution confinement failure (started=false, nothing
// consumed, no "running" progress).
//
// Platform differences recorded, not equalized: there is no SIGTERM-like
// graceful stage — cancellation is immediate TerminateJobObject; and an
// external TerminateProcess is not runtime-distinguishable from an ordinary
// nonzero exit, so the POSIX signal-termination UNKNOWN row has no Windows
// runtime-verified equivalent (see terminatedBySignal below).

var groupTracker = struct {
	mu     sync.Mutex
	groups map[*exec.Cmd]*jobState
}{groups: map[*exec.Cmd]*jobState{}}

type jobState struct {
	job windows.Handle
}

// API seams (R1-CX-F1 recommendation 5): deterministic failure fixtures
// inject enumeration/open/verify/resume failures without real API races.
// Production leaves these at the x/sys implementations.
var (
	createToolhelpSnapshot = windows.CreateToolhelp32Snapshot
	thread32First          = windows.Thread32First
	thread32Next           = windows.Thread32Next
	openThread             = windows.OpenThread
	getProcessIdOfThread   = getProcessIdOfThreadImpl
	resumeThread           = windows.ResumeThread
)

// GetProcessIdOfThread is a documented kernel32 API that x/sys v0.47.0 does
// not wrap; this is the single lazy-proc exception to the x/sys-only rule
// (AR-1 keeps the raw surface minimal — R1 review item). It re-verifies the
// opened thread handle's owner PID against TID reuse.
var procGetProcessIdOfThread = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessIdOfThread")

func getProcessIdOfThreadImpl(h windows.Handle) (uint32, error) {
	r, _, callErr := procGetProcessIdOfThread.Call(uintptr(h))
	if r == 0 {
		return 0, fmt.Errorf("GetProcessIdOfThread: %w", callErr)
	}
	return uint32(r), nil
}

// newGroupCmd builds an exec.Cmd whose child starts suspended; trackGroup
// completes the confinement (job assign + resume). Cancellation terminates
// the whole job immediately — Windows has no graceful-termination stage and
// none is claimed. The grace duration only pads WaitDelay.
func newGroupCmd(ctx context.Context, grace time.Duration, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	var cancelOnce sync.Once
	cmd.Cancel = func() error {
		var err error
		cancelOnce.Do(func() {
			if cmd.Process == nil {
				return
			}
			groupTracker.mu.Lock()
			g, ok := groupTracker.groups[cmd]
			groupTracker.mu.Unlock()
			if ok {
				err = windows.TerminateJobObject(g.job, 1)
			} else {
				// Cancel raced ahead of the tracker registration: the child
				// is either still suspended (pre-assign) or the confinement
				// failure path is already terminating it, so direct
				// termination cannot orphan descendants.
				err = cmd.Process.Kill()
			}
		})
		return err
	}
	cmd.WaitDelay = grace + 2*time.Second // backstop for the direct child
	return cmd
}

// trackGroup confines the suspended child: create job (kill-on-close),
// assign, register the tracker entry, then resume the initial thread. The
// entry is registered immediately after a successful assign (R1-CX-F1
// recommendation 4) so a concurrent Cancel takes the job-terminate path; a
// resume failure rolls the entry back, terminates the job, and closes the
// handle. Any failure returns a confinement error — the child never ran.
func trackGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := uint32(cmd.Process.Pid)
	fail := func(stage string, err error) error {
		_ = cmd.Process.Kill()
		return fmt.Errorf("windows job confinement failed before child execution (%s): %w", stage, err)
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fail("create job", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return fail("set kill-on-close", err)
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		windows.CloseHandle(job)
		return fail("open process", err)
	}
	assignErr := windows.AssignProcessToJobObject(job, proc)
	windows.CloseHandle(proc)
	if assignErr != nil {
		windows.CloseHandle(job)
		return fail("assign to job", assignErr)
	}
	// Register before resume: from here a racing Cancel terminates the job.
	groupTracker.mu.Lock()
	groupTracker.groups[cmd] = &jobState{job: job}
	groupTracker.mu.Unlock()
	if err := resumeInitialThread(pid); err != nil {
		groupTracker.mu.Lock()
		delete(groupTracker.groups, cmd)
		groupTracker.mu.Unlock()
		_ = windows.TerminateJobObject(job, 1)
		windows.CloseHandle(job)
		// The child is inside the terminated job; Kill in fail() is a
		// harmless idempotent backstop.
		return fail("resume initial thread", err)
	}
	return nil
}

// enumerateInitialThread returns the single thread ID owned by pid. The
// enumeration is fail-closed: it must terminate with ERROR_NO_MORE_FILES and
// yield exactly one thread — zero, multiple, or any other enumeration error
// rejects the recovery (R1-CX-F1 recommendations 1 and 3 preconditions).
func enumerateInitialThread(pid uint32) (uint32, error) {
	snap, err := createToolhelpSnapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return 0, fmt.Errorf("thread snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)
	var entry windows.ThreadEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var threadIDs []uint32
	err = thread32First(snap, &entry)
	for err == nil {
		if entry.OwnerProcessID == pid {
			threadIDs = append(threadIDs, entry.ThreadID)
		}
		err = thread32Next(snap, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return 0, fmt.Errorf("thread enumeration ended abnormally: fail-closed: %w", err)
	}
	if len(threadIDs) != 1 {
		return 0, fmt.Errorf("suspended child pid %d has %d threads in snapshot (exactly one initial thread required): fail-closed", pid, len(threadIDs))
	}
	return threadIDs[0], nil
}

// resumeInitialThread resumes the suspended child's initial thread under the
// strict recovery rules: single-thread enumeration, owner-PID re-verification
// on the opened handle (TID reuse defense), and a previous suspend count of
// exactly 1 — 0 means the child was already running and >1 means it stays
// suspended; both are confinement failures.
func resumeInitialThread(pid uint32) error {
	tid, err := enumerateInitialThread(pid)
	if err != nil {
		return err
	}
	th, err := openThread(windows.THREAD_SUSPEND_RESUME|windows.THREAD_QUERY_LIMITED_INFORMATION, false, tid)
	if err != nil {
		return fmt.Errorf("open initial thread: fail-closed: %w", err)
	}
	defer windows.CloseHandle(th)
	if owner, err := getProcessIdOfThread(th); err != nil {
		return fmt.Errorf("verify thread owner: fail-closed: %w", err)
	} else if owner != pid {
		return fmt.Errorf("opened thread belongs to pid %d, not %d (TID reuse): fail-closed", owner, pid)
	}
	prev, err := resumeThread(th)
	if err != nil {
		return fmt.Errorf("resume initial thread: fail-closed: %w", err)
	}
	if prev != 1 {
		return fmt.Errorf("resume previous suspend count %d (exactly 1 required — 0 means already running, >1 stays suspended): fail-closed", prev)
	}
	return nil
}

// releaseGroup closes the job handle after the direct child is reaped. The
// job carries kill-on-close, so closing the last handle terminates any
// remaining descendants — the no-orphan guarantee on Windows.
func releaseGroup(cmd *exec.Cmd) {
	groupTracker.mu.Lock()
	defer groupTracker.mu.Unlock()
	if g, ok := groupTracker.groups[cmd]; ok {
		windows.CloseHandle(g.job)
		delete(groupTracker.groups, cmd)
	}
}

// ForceKillActiveProcessGroups terminates every tracked job immediately. It
// is the CLI's second-signal escape hatch; classification and durable
// recording stay with the journal/reconcile path.
func ForceKillActiveProcessGroups() {
	groupTracker.mu.Lock()
	defer groupTracker.mu.Unlock()
	for _, g := range groupTracker.groups {
		_ = windows.TerminateJobObject(g.job, 1)
	}
}

// ActiveProcessGroupCount is a test observability hook.
func ActiveProcessGroupCount() int {
	groupTracker.mu.Lock()
	defer groupTracker.mu.Unlock()
	return len(groupTracker.groups)
}

// terminatedBySignal has no Windows runtime-verified equivalent: an external
// TerminateProcess is indistinguishable from an ordinary nonzero exit, so
// this always reports false and external kills classify through the
// missing-terminal FAILED row instead of the POSIX UNKNOWN row. This is a
// documented platform difference, not an equivalence claim; parent-signal
// and hard-cap ambiguity classification are unaffected (they classify from
// the cancellation cause, not from the exit status).
func terminatedBySignal(cmd *exec.Cmd) bool {
	return false
}
