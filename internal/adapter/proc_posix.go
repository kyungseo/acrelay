//go:build darwin || linux

package adapter

import (
	"context"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// groupTracker owns every live child process group and its single escalation
// timer. Ownership is exclusive: Cancel arms the timer at most once, and the
// post-Wait release stops it so a reused pgid is never signaled after reap
// (R0-CX-F5 — the inherent POSIX pid-reuse window during the grace interval
// remains a documented residual risk).
var groupTracker = struct {
	mu     sync.Mutex
	groups map[*exec.Cmd]*groupState
}{groups: map[*exec.Cmd]*groupState{}}

type groupState struct {
	pgid  int
	timer *time.Timer
}

// trackGroup registers the started child's process group. On POSIX the group
// membership was established at fork via Setpgid, so tracking never fails —
// the error return exists for the shared cross-platform confinement contract
// (Windows performs fallible job assignment here).
func trackGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	groupTracker.mu.Lock()
	defer groupTracker.mu.Unlock()
	// get-or-create (R1-CX-F4): a Cancel racing ahead of trackGroup may have
	// already created the entry and armed the escalation timer. Overwriting
	// would orphan that timer, so preserve any existing state.
	if _, ok := groupTracker.groups[cmd]; !ok {
		groupTracker.groups[cmd] = &groupState{pgid: cmd.Process.Pid}
	}
	return nil
}

func releaseGroup(cmd *exec.Cmd) {
	groupTracker.mu.Lock()
	defer groupTracker.mu.Unlock()
	if g, ok := groupTracker.groups[cmd]; ok {
		if g.timer != nil {
			g.timer.Stop()
		}
		delete(groupTracker.groups, cmd)
	}
}

func armEscalation(cmd *exec.Cmd, grace time.Duration, pgid int) {
	groupTracker.mu.Lock()
	defer groupTracker.mu.Unlock()
	g, ok := groupTracker.groups[cmd]
	if !ok {
		// Cancel is invoked by exec.CommandContext only while the child is
		// still being waited on; the inherent POSIX window between reap and
		// timer stop remains a documented residual risk (R0-CX-F5).
		g = &groupState{pgid: pgid}
		groupTracker.groups[cmd] = g
	}
	if g.timer != nil { // already armed: idempotent
		return
	}
	g.timer = time.AfterFunc(grace, func() { escalationKill(pgid) })
}

// escalationKill is the group SIGKILL escalation. It is a package var so a
// test can observe whether an orphaned timer fires after release (R2-CX-F2):
// with the correct get-or-create trackGroup the timer is stopped on release
// and this never runs; the reverted overwrite implementation orphans the
// timer and this fires against the reaped pgid.
var escalationKill = func(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// ForceKillActiveProcessGroups SIGKILLs every tracked child group. It is the
// CLI's second-signal escape hatch; classification and durable recording stay
// with the journal/reconcile path, never with this helper.
func ForceKillActiveProcessGroups() {
	groupTracker.mu.Lock()
	defer groupTracker.mu.Unlock()
	for _, g := range groupTracker.groups {
		if g.timer != nil {
			g.timer.Stop()
		}
		_ = syscall.Kill(-g.pgid, syscall.SIGKILL)
	}
}

// ActiveProcessGroupCount is a test observability hook for timer/group
// lifecycle assertions.
func ActiveProcessGroupCount() int {
	groupTracker.mu.Lock()
	defer groupTracker.mu.Unlock()
	return len(groupTracker.groups)
}

// newGroupCmd builds an exec.Cmd whose child runs in an isolated,
// terminable lifecycle boundary. On POSIX this is a new process group
// (the Windows Job Object port lives in proc_windows.go).
// On cancellation the whole group gets SIGTERM, then SIGKILL after grace —
// grandchildren that ignore SIGTERM do not survive (R0-CX-F8). Cancel is
// idempotent and the escalation timer is owned by the group tracker.
func newGroupCmd(ctx context.Context, grace time.Duration, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var cancelOnce sync.Once
	cmd.Cancel = func() error {
		var err error
		cancelOnce.Do(func() {
			if cmd.Process == nil {
				return
			}
			pgid := cmd.Process.Pid
			err = syscall.Kill(-pgid, syscall.SIGTERM)
			armEscalation(cmd, grace, pgid)
		})
		return err
	}
	cmd.WaitDelay = grace + 2*time.Second // backstop for the direct child
	return cmd
}

// terminatedBySignal reports whether the reaped child died from a signal —
// the runtime-verified fact behind the UNKNOWN classification rows of the
// FEAT-20260721-002 precedence table.
func terminatedBySignal(cmd *exec.Cmd) bool {
	if cmd.ProcessState == nil {
		return false
	}
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}
