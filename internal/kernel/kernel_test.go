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
		{ExecPrepared, ExecSucceeded},         // skipping dispatch
		{ExecSucceeded, ExecRunning},          // terminal has no exit
		{ExecUnknown, ExecRunning},            // UNKNOWN is terminal
		{ExecFailed, ExecDispatched},          // no automatic retry via state machine
		{ExecutionState("bogus"), ExecFailed}, // unknown source fails closed
	}
	for _, v := range invalid {
		if err := ValidateExecTransition(v[0], v[1]); err == nil {
			t.Errorf("expected invalid %s->%s to fail closed", v[0], v[1])
		}
	}
}

func TestGovTransitions(t *testing.T) {
	if err := ValidateGovTransition(GovOpen, GovDecisionRequired); err != nil {
		t.Fatal(err)
	}
	invalid := [][2]GovernanceState{
		{GovOpen, GovClosed}, // must pass through CLOSABLE
		{GovClosed, GovOpen}, // terminal has no exit
		{GovernanceState("mystery"), GovClosed}, // unknown state not-closable
	}
	for _, v := range invalid {
		if err := ValidateGovTransition(v[0], v[1]); err == nil {
			t.Errorf("expected invalid %s->%s to fail closed", v[0], v[1])
		}
	}
}

func TestRoundBound(t *testing.T) {
	o := &Objective{ID: "obj-1", Governance: GovOpen}
	for i := 0; i < MaxRoundsPerObjective; i++ {
		if _, err := o.OpenRound(); err != nil {
			t.Fatalf("round %d should open: %v", i, err)
		}
	}
	if _, err := o.OpenRound(); err == nil {
		t.Fatal("R3 must be refused — owner decision gate required")
	} else if !strings.Contains(err.Error(), "owner decision gate") {
		t.Fatalf("R3 refusal must point to owner gate: %v", err)
	}
}

func TestTerminalObjectiveRefusesRounds(t *testing.T) {
	o := &Objective{ID: "obj-t", Governance: GovAbandoned}
	if _, err := o.OpenRound(); err == nil {
		t.Fatal("terminal objective must refuse new rounds")
	}
}

func TestAttemptBoundAndUnknown(t *testing.T) {
	r := &Round{}
	a1, err := r.OpenAttempt()
	if err != nil {
		t.Fatal(err)
	}
	// UNKNOWN forbids any further attempt.
	mustTransition(t, a1, ExecDispatched, ExecUnknown)
	if _, err := r.OpenAttempt(); err == nil {
		t.Fatal("attempt after UNKNOWN must be refused")
	}

	// FAILED allows exactly one more attempt, then the bound applies.
	r2 := &Round{}
	b1, _ := r2.OpenAttempt()
	mustTransition(t, b1, ExecDispatched, ExecFailed)
	b2, err := r2.OpenAttempt()
	if err != nil {
		t.Fatalf("second attempt after FAILED should open: %v", err)
	}
	mustTransition(t, b2, ExecDispatched, ExecFailed)
	if _, err := r2.OpenAttempt(); err == nil {
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

func TestConfirmationCycle(t *testing.T) {
	r := &Round{}
	c, err := r.OpenConfirmation([]string{"F1", "F2", "F3"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.OpenConfirmation(nil); err == nil {
		t.Fatal("second confirmation cycle in one round must be refused")
	}

	// Precondition failure consumes no valid attempt (CP-1 fixture).
	c.RecordPreconditionFailure()
	if c.ValidAttempts != 0 {
		t.Fatal("precondition failure must not consume a valid attempt")
	}

	// Valid attempt 1 confirms F1 only.
	if err := c.SubmitValidAttempt([]string{"F1", "F2", "F3"}, []string{"F1"}); err != nil {
		t.Fatal(err)
	}
	// Resubmission may only carry outstanding IDs.
	if err := c.SubmitValidAttempt([]string{"F1"}, nil); err == nil {
		t.Fatal("resubmitting a confirmed ID must be refused")
	}
	if err := c.SubmitValidAttempt([]string{"F2", "F3"}, []string{"F2"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SubmitValidAttempt([]string{"F3"}, nil); err != nil {
		t.Fatal(err)
	}
	if !c.Escalated {
		t.Fatal("unresolved after max valid attempts must escalate to owner gate")
	}
	if err := c.SubmitValidAttempt([]string{"F3"}, []string{"F3"}); err == nil {
		t.Fatal("escalated cycle must refuse further attempts")
	}
}

func TestSameTargetLink(t *testing.T) {
	o := &Objective{ID: "obj-2"}
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

func TestTerminalReason(t *testing.T) {
	if err := (TerminalReason{}).Validate(); err == nil {
		t.Fatal("terminal transition without arbiter/reason must fail closed")
	}
	if err := (TerminalReason{Arbiter: "owner", Reason: "superseded by obj-3"}).Validate(); err != nil {
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
	if !strings.HasPrefix(a, "sref-") {
		t.Fatalf("unexpected prefix: %s", a)
	}
}
