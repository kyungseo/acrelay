package kernel

import (
	"strings"
	"testing"
)

func TestExecTransitions(t *testing.T) {
	valid := [][2]ExecutionState{
		{ExecPrepared, ExecDispatched},
		{ExecPrepared, ExecFailed}, // pre-dispatch validation failure
		{ExecDispatched, ExecRunning},
		{ExecRunning, ExecSucceeded},
		{ExecRunning, ExecUnknown},
	}
	for _, v := range valid {
		if err := ValidateExecTransition(v[0], v[1]); err != nil {
			t.Errorf("expected valid %s->%s: %v", v[0], v[1], err)
		}
	}
	invalid := [][2]ExecutionState{
		{ExecPrepared, ExecSucceeded},
		{ExecSucceeded, ExecRunning},
		{ExecUnknown, ExecRunning},
		{ExecFailed, ExecDispatched},
		{ExecutionState("bogus"), ExecFailed},
	}
	for _, v := range invalid {
		if err := ValidateExecTransition(v[0], v[1]); err == nil {
			t.Errorf("expected invalid %s->%s to fail closed", v[0], v[1])
		}
	}
}

// F2: governance is only reachable through contract methods.
func TestGovernanceMethodsAtomicity(t *testing.T) {
	// zero-value objective: unknown governance fails closed everywhere
	var zero Objective
	if _, err := zero.OpenRound(); err == nil {
		t.Fatal("zero-value objective must not admit rounds")
	}
	if err := zero.RequireDecision(); err == nil {
		t.Fatal("zero-value governance transition must fail closed")
	}

	o := NewObjective("obj-1", "collab-1", "q", "rev-a")
	if o.Governance() != GovOpen {
		t.Fatal("new objective must be OPEN")
	}
	// terminal without arbiter/reason is impossible
	if err := o.Terminate(GovAbandoned, TerminalReason{}); err == nil {
		t.Fatal("terminate without arbiter/reason must fail closed")
	}
	// terminate to CLOSED is not a terminate path
	if err := o.Terminate(GovClosed, TerminalReason{Arbiter: "owner", Reason: "r"}); err == nil {
		t.Fatal("terminate to CLOSED must be refused")
	}
	// close is only reachable from CLOSABLE and only with a passing check
	if err := o.Close(func() error { return nil }); err == nil {
		t.Fatal("close from OPEN must be refused")
	}
	if err := o.MarkClosable(); err != nil {
		t.Fatal(err)
	}
	if err := o.Close(nil); err == nil {
		t.Fatal("close without closure check must fail closed")
	}
	if err := o.Close(func() error { return errTest }); err == nil {
		t.Fatal("failing closure check must block close")
	}
	if err := o.Close(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if o.Governance() != GovClosed {
		t.Fatal("close must set CLOSED")
	}
	// terminal is terminal
	if err := o.RequireDecision(); err == nil {
		t.Fatal("CLOSED must have no outgoing transitions")
	}

	// terminate path records reason atomically
	o2 := NewObjective("obj-2", "collab-1", "q", "rev-a")
	if err := o2.Terminate(GovAbandoned, TerminalReason{Arbiter: "owner", Reason: "scope cut"}); err != nil {
		t.Fatal(err)
	}
	if o2.Terminal() == nil || o2.Terminal().Arbiter != "owner" {
		t.Fatal("terminal reason must be recorded with the transition")
	}
	if _, err := o2.OpenRound(); err == nil {
		t.Fatal("terminal objective must refuse rounds")
	}
}

var errTest = &testError{}

type testError struct{}

func (*testError) Error() string { return "unresolved blocking finding" }

func TestRoundBound(t *testing.T) {
	o := NewObjective("obj-1", "c", "q", "rev")
	for i := 0; i < MaxRoundsPerObjective; i++ {
		if _, err := o.OpenRound(); err != nil {
			t.Fatalf("round %d should open: %v", i, err)
		}
	}
	if _, err := o.OpenRound(); err == nil {
		t.Fatal("R3 must be refused — owner decision gate required")
	}
}

// F3: prepared attempts consume nothing until CommitDispatch.
func TestPreparedAttemptConsumesNothing(t *testing.T) {
	r := &Round{}
	for i := 0; i < 10; i++ {
		a := r.PrepareAttempt()
		// preflight failure path: transition PREPARED -> FAILED, never commit
		if err := a.Transition(ExecFailed); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.Attempts) != 0 {
		t.Fatal("preflight failures must not consume the attempt budget")
	}
	a := r.PrepareAttempt()
	if err := r.CommitDispatch(a); err != nil {
		t.Fatal(err)
	}
	if len(r.Attempts) != 1 || a.State != ExecDispatched {
		t.Fatal("commit must admit the attempt at DISPATCHED")
	}
	// committing a non-prepared attempt is refused
	if err := r.CommitDispatch(a); err == nil {
		t.Fatal("double commit must be refused")
	}
}

func TestAttemptBoundAndUnknown(t *testing.T) {
	r := &Round{}
	a1 := r.PrepareAttempt()
	if err := r.CommitDispatch(a1); err != nil {
		t.Fatal(err)
	}
	mustTransition(t, a1, ExecUnknown)
	if err := r.CommitDispatch(r.PrepareAttempt()); err == nil {
		t.Fatal("attempt after UNKNOWN must be refused")
	}

	r2 := &Round{}
	b1 := r2.PrepareAttempt()
	r2.CommitDispatch(b1)
	mustTransition(t, b1, ExecFailed)
	b2 := r2.PrepareAttempt()
	if err := r2.CommitDispatch(b2); err != nil {
		t.Fatalf("second attempt after FAILED should commit: %v", err)
	}
	mustTransition(t, b2, ExecFailed)
	if err := r2.CommitDispatch(r2.PrepareAttempt()); err == nil {
		t.Fatal("third attempt must exceed the per-round bound")
	}
}

func mustTransition(t *testing.T, a *Attempt, states ...ExecutionState) {
	t.Helper()
	for _, s := range states {
		if err := a.Transition(s); err != nil {
			t.Fatal(err)
		}
	}
}

// F1: confirmation cycle boundary hardening.
func TestConfirmationCycleHardening(t *testing.T) {
	r := &Round{}
	if _, err := r.OpenConfirmation(nil); err == nil {
		t.Fatal("empty initial set must be refused")
	}
	if _, err := r.OpenConfirmation([]string{"F1", "F1"}); err == nil {
		t.Fatal("duplicate initial IDs must be refused")
	}
	if _, err := r.OpenConfirmation([]string{" "}); err == nil {
		t.Fatal("blank initial ID must be refused")
	}
	c, err := r.OpenConfirmation([]string{"F1", "F2", "F3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.OpenConfirmation([]string{"F9"}); err == nil {
		t.Fatal("second cycle in one round must be refused")
	}

	c.RecordPreconditionFailure()
	if c.ValidAttempts != 0 {
		t.Fatal("precondition failure must not consume a valid attempt")
	}

	// out-of-set ID refused even on the first attempt
	if err := c.SubmitValidAttempt([]string{"F1", "F9"}, nil); err == nil {
		t.Fatal("ID outside the initial closed set must be refused")
	}
	// empty submission refused
	if err := c.SubmitValidAttempt(nil, nil); err == nil {
		t.Fatal("empty submission must be refused")
	}
	// confirmed must be subset of submitted
	if err := c.SubmitValidAttempt([]string{"F1"}, []string{"F2"}); err == nil {
		t.Fatal("confirmed outside the submission must be refused")
	}
	if c.ValidAttempts != 0 {
		t.Fatal("rejected submissions must not consume valid attempts")
	}

	// partial submission: unsubmitted findings remain outstanding
	if err := c.SubmitValidAttempt([]string{"F2"}, []string{"F2"}); err != nil {
		t.Fatal(err)
	}
	out := c.Outstanding()
	if len(out) != 2 || out[0] != "F1" || out[1] != "F3" {
		t.Fatalf("unsubmitted findings must stay outstanding: %v", out)
	}
	// confirmed ID no longer resubmittable
	if err := c.SubmitValidAttempt([]string{"F2"}, nil); err == nil {
		t.Fatal("confirmed ID must not be outstanding")
	}
	// attempts 2..3 without full confirmation escalate
	if err := c.SubmitValidAttempt([]string{"F1"}, []string{"F1"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SubmitValidAttempt([]string{"F3"}, nil); err != nil {
		t.Fatal(err)
	}
	if !c.Escalated || c.Done() {
		t.Fatal("unresolved after max valid attempts must escalate")
	}
	if err := c.SubmitValidAttempt([]string{"F3"}, []string{"F3"}); err == nil {
		t.Fatal("escalated cycle must refuse further attempts")
	}
}

func TestConfirmationCycleCompletes(t *testing.T) {
	r := &Round{}
	c, _ := r.OpenConfirmation([]string{"F1"})
	if err := c.SubmitValidAttempt([]string{"F1"}, []string{"F1"}); err != nil {
		t.Fatal(err)
	}
	if !c.Done() || c.Escalated {
		t.Fatal("fully confirmed cycle must complete without escalation")
	}
	if err := c.SubmitValidAttempt([]string{"F1"}, nil); err == nil {
		t.Fatal("completed cycle must refuse attempts")
	}
}

func TestSameTargetLink(t *testing.T) {
	o := NewObjective("obj-2", "c", "q", "rev")
	if err := o.ValidateSameTargetLink(false); err != nil {
		t.Fatal("fresh target needs no prior pointer")
	}
	if err := o.ValidateSameTargetLink(true); err == nil {
		t.Fatal("same-target objective without prior pointer must fail closed")
	}
	o.PriorObjective, o.MaterialDifference = "obj-1", "target scope narrowed to store layer"
	if err := o.ValidateSameTargetLink(true); err != nil {
		t.Fatal(err)
	}
}

func TestNewIDRandomness(t *testing.T) {
	a, err := NewID("sref")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewID("sref")
	if a == b {
		t.Fatal("IDs must be random")
	}
	if !strings.HasPrefix(a, "sref-") || len(a) != len("sref-")+32 {
		t.Fatalf("expected 128-bit hex ID: %s", a)
	}
}
