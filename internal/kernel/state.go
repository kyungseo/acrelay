// Package kernel defines the platform-neutral collaboration contract:
// identity types, the execution/governance dual state machines, and the
// round/attempt bounds fixed by DR-811.
package kernel

import "fmt"

// ExecutionState tracks one adapter invocation. It never implies governance
// progress: SUCCEEDED means "invocation and result validation finished",
// nothing more.
type ExecutionState string

const (
	ExecPrepared   ExecutionState = "PREPARED"
	ExecDispatched ExecutionState = "DISPATCHED"
	ExecRunning    ExecutionState = "RUNNING"
	ExecSucceeded  ExecutionState = "SUCCEEDED"
	ExecFailed     ExecutionState = "FAILED"
	ExecCanceled   ExecutionState = "CANCELED"
	ExecUnknown    ExecutionState = "UNKNOWN"
)

// GovernanceState tracks an objective's decision lifecycle.
type GovernanceState string

const (
	GovOpen             GovernanceState = "OPEN"
	GovDecisionRequired GovernanceState = "DECISION_REQUIRED"
	GovClosable         GovernanceState = "CLOSABLE"
	GovClosed           GovernanceState = "CLOSED"
	GovSuperseded       GovernanceState = "SUPERSEDED"
	GovAbandoned        GovernanceState = "ABANDONED"
)

// Bounds fixed by the v1 contract. The formal round bound is selected per
// objective; attempt and confirmation bounds remain compile-time constants.
const (
	MinFormalRoundBound         = 1
	DefaultFormalRoundBound     = 3
	MaxFormalRoundBound         = 5
	MaxAttemptsPerRound         = 2
	MaxValidConfirmationRetries = 3 // valid attempts per confirmation cycle
)

// ValidateFormalRoundBound rejects values outside the owner-selectable range.
func ValidateFormalRoundBound(bound int) error {
	if bound < MinFormalRoundBound || bound > MaxFormalRoundBound {
		return fmt.Errorf("formal round bound %d outside supported range %d..%d: fail-closed",
			bound, MinFormalRoundBound, MaxFormalRoundBound)
	}
	return nil
}

var execTransitions = map[ExecutionState][]ExecutionState{
	ExecPrepared:   {ExecDispatched, ExecFailed}, // pre-dispatch validation failure
	ExecDispatched: {ExecRunning, ExecFailed, ExecUnknown},
	ExecRunning:    {ExecSucceeded, ExecFailed, ExecCanceled, ExecUnknown},
	// terminal states have no outgoing transitions; UNKNOWN is terminal —
	// it must never be silently re-driven (no automatic retry).
}

var govTransitions = map[GovernanceState][]GovernanceState{
	GovOpen:             {GovDecisionRequired, GovClosable, GovSuperseded, GovAbandoned},
	GovDecisionRequired: {GovClosable, GovSuperseded, GovAbandoned},
	GovClosable:         {GovClosed, GovDecisionRequired, GovSuperseded, GovAbandoned},
	// CLOSED / SUPERSEDED / ABANDONED are terminal.
}

// ValidateExecTransition returns an error for any transition outside the
// contract. Unknown source states fail closed.
func ValidateExecTransition(from, to ExecutionState) error {
	for _, ok := range execTransitions[from] {
		if to == ok {
			return nil
		}
	}
	return fmt.Errorf("invalid execution transition %s -> %s: fail-closed", from, to)
}

// ValidateGovTransition returns an error for any transition outside the
// contract. Reaching CLOSED additionally requires ClosureCheck to pass;
// this function only guards the state graph.
func ValidateGovTransition(from, to GovernanceState) error {
	for _, ok := range govTransitions[from] {
		if to == ok {
			return nil
		}
	}
	return fmt.Errorf("invalid governance transition %s -> %s: fail-closed", from, to)
}

// IsGovTerminal reports whether a governance state ends the objective.
func IsGovTerminal(s GovernanceState) bool {
	return s == GovClosed || s == GovSuperseded || s == GovAbandoned
}

// TerminalReason records why an arbiter ended an objective without closure.
// Automatic close/expiry is forbidden: SUPERSEDED and ABANDONED always carry
// an arbiter identity and reason.
type TerminalReason struct {
	Arbiter string
	Reason  string
}

// Validate rejects empty arbiter or reason: an objective may never end
// terminally without a recorded authority and rationale.
func (t TerminalReason) Validate() error {
	if t.Arbiter == "" || t.Reason == "" {
		return fmt.Errorf("terminal transition requires arbiter and reason: fail-closed")
	}
	return nil
}
