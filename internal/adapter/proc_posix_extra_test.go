//go:build darwin || linux

package adapter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// FEAT-20260721-002 R1-CX-F4/R2-CX-F2: a Cancel that arms the escalation
// timer before trackGroup registration must not be orphaned — releaseGroup
// must stop it so the SIGKILL escalation never fires after the child is
// reaped. This observes the escalation callback directly, so reverting
// trackGroup to the unconditional-overwrite implementation makes it fail.
func TestEscalationTimerSurvivesStartTrackInterleaving(t *testing.T) {
	origHook := beforeTrackGroupHook
	origKill := escalationKill
	t.Cleanup(func() { beforeTrackGroupHook = origHook; escalationKill = origKill })

	var killMu sync.Mutex
	var killed []int
	escalationKill = func(pgid int) {
		killMu.Lock()
		killed = append(killed, pgid)
		killMu.Unlock()
	}

	baseline := ActiveProcessGroupCount()
	ctx, cancel := context.WithCancel(context.Background())
	// Force Cancel to run (arming the timer via armEscalation) after Start but
	// before trackGroup, reproducing the interleaving.
	beforeTrackGroupHook = func(cmd *exec.Cmd) {
		cancel()
		_ = cmd.Cancel() // idempotent; arms the escalation timer
	}
	// A short grace so a leaked timer would fire well within the test window;
	// the child exits on the Cancel SIGTERM before grace elapses.
	grace := 150 * time.Millisecond
	cmd := newGroupCmd(ctx, grace, "sh", "-c", "sleep 5")
	_ = runWithProgress(cmd, nil)

	// The interleaved-armed timer must not be orphaned: releaseGroup removed
	// this cmd's entry and stopped its timer, returning to baseline.
	if got := ActiveProcessGroupCount(); got != baseline {
		t.Fatalf("group tracker leaked: count %d, baseline %d", got, baseline)
	}
	if cmd.ProcessState == nil {
		t.Fatal("child must have been reaped after interleaved cancellation")
	}
	// Wait well past grace: a stopped timer never fires; an orphaned one would.
	time.Sleep(3 * grace)
	killMu.Lock()
	fired := append([]int(nil), killed...)
	killMu.Unlock()
	if len(fired) != 0 {
		t.Fatalf("escalation timer fired after release (orphaned timer): pgids=%v", fired)
	}
}

// R1-CX-F6 item 3: a real OS SIGINT delivered to a helper process must, via a
// real signal handler, cancel the dispatch context and terminate the grouped
// child — the actual signal→context→group-kill path end to end.
func TestRealSignalTerminatesGroupedChild(t *testing.T) {
	if os.Getenv("ACRELAY_SIGNAL_HELPER") == "1" {
		runSignalHelperChild()
		return
	}
	marker := filepath.Join(t.TempDir(), "childpid")
	helper := exec.Command(os.Args[0], "-test.run=TestRealSignalTerminatesGroupedChild$")
	helper.Env = append(os.Environ(), "ACRELAY_SIGNAL_HELPER=1", "ACRELAY_SIGNAL_MARKER="+marker)
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && childPID == 0 {
		if b, err := os.ReadFile(marker); err == nil {
			fmt.Sscanf(string(b), "%d", &childPID)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPID == 0 {
		helper.Process.Kill()
		t.Fatal("helper never reported a running grouped child")
	}
	if err := helper.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	time.Sleep(500 * time.Millisecond)
	if err := syscall.Kill(childPID, 0); err == nil {
		syscall.Kill(childPID, syscall.SIGKILL)
		t.Fatalf("grouped child %d survived the real SIGINT cancellation", childPID)
	}
}

// runSignalHelperChild runs in the helper process: it installs a real signal
// handler that cancels the dispatch context on SIGINT, starts a grouped child
// via the production newGroupCmd, records the child PID, and exits once the
// signal-cancelled Wait returns.
func runSignalHelperChild() {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; cancel() }()

	cmd := newGroupCmd(ctx, 200*time.Millisecond, "sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		os.Exit(3)
	}
	trackGroup(cmd)
	defer releaseGroup(cmd)
	if marker := os.Getenv("ACRELAY_SIGNAL_MARKER"); marker != "" {
		os.WriteFile(marker, []byte(fmt.Sprintf("%d", cmd.Process.Pid)), 0o600)
	}
	_ = cmd.Wait() // returns after the signal cancels ctx and the group is killed
	os.Exit(1)
}

// R0-CX-F8: SIGTERM-ignoring grandchildren die at grace escalation.
func TestGroupKillGraceEscalation(t *testing.T) {
	marker := "1799" // unique sleep duration as process marker
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	cmd := newGroupCmd(ctx, 500*time.Millisecond, "bash", "-c",
		"trap '' TERM; sleep "+marker+" & sleep "+marker+" & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	time.Sleep(300 * time.Millisecond) // let children spawn
	<-done                             // ctx timeout → TERM (ignored) → grace → group SIGKILL
	time.Sleep(700 * time.Millisecond) // allow the AfterFunc SIGKILL to land
	out, _ := exec.Command("pgrep", "-f", "sleep "+marker).Output()
	if len(strings.TrimSpace(string(out))) != 0 {
		exec.Command("pkill", "-9", "-f", "sleep "+marker).Run()
		t.Fatalf("grandchildren survived grace escalation: %q", out)
	}
}
