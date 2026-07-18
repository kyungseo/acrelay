package kernel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// Collaboration is the portable lifecycle joining one or more objectives.
// The reviewer execution session is scoped to this lifecycle (DR-811 §1):
// related objectives across linked works reuse the same session by
// sequential resume.
type Collaboration struct {
	ID         string
	Objectives []*Objective
}

// Objective is one question to decide against a specific target revision.
type Objective struct {
	ID              string
	CollaborationID string
	Question        string
	TargetRevision  string // pre-dispatch snapshot of the target manifest
	Governance      GovernanceState
	Rounds          []*Round
	// PriorObjective and MaterialDifference are mandatory when this
	// objective references the same target manifest as an earlier one.
	// The relay validates presence only, never meaning.
	PriorObjective     string
	MaterialDifference string
	Terminal           *TerminalReason
}

// Round is one formal reviewer assessment (R0..R2).
type Round struct {
	Index    int // 0-based
	Attempts []*Attempt
	// Confirmation is the at-most-one confirmation cycle for this round.
	Confirmation *ConfirmationCycle
}

// Attempt is one transport/adapter invocation.
type Attempt struct {
	Index int
	State ExecutionState
}

// ConfirmationCycle implements the CG-1 contract: precondition failures do
// not consume valid attempts; resubmission may only carry a subset of
// not-confirmed IDs; at most MaxValidConfirmationRetries valid attempts.
type ConfirmationCycle struct {
	ValidAttempts        int
	PreconditionFailures int
	OutstandingIDs       []string // not-confirmed finding IDs still open
	Escalated            bool     // unresolved after max attempts -> owner gate
}

// NewID returns a cryptographically random identifier with the given prefix.
// Identifiers are never derived from vendor handles or other stable IDs
// (DR-811 §3).
func NewID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b), nil
}

// OpenRound starts the next formal round, enforcing the R0..R2 bound.
// After R2 the only paths are the arbiter decisions handled at governance
// level; a fourth round is never created.
func (o *Objective) OpenRound() (*Round, error) {
	if IsGovTerminal(o.Governance) {
		return nil, fmt.Errorf("objective %s is terminal (%s): fail-closed", o.ID, o.Governance)
	}
	if len(o.Rounds) >= MaxRoundsPerObjective {
		return nil, fmt.Errorf("round bound exceeded (max R0..R%d): owner decision gate required", MaxRoundsPerObjective-1)
	}
	r := &Round{Index: len(o.Rounds)}
	o.Rounds = append(o.Rounds, r)
	return r, nil
}

// OpenAttempt starts a new attempt in the round. A second attempt is
// forbidden after an UNKNOWN attempt (no automatic re-dispatch after an
// ambiguous outcome) and after the per-round attempt bound.
func (r *Round) OpenAttempt() (*Attempt, error) {
	if len(r.Attempts) >= MaxAttemptsPerRound {
		return nil, fmt.Errorf("attempt bound exceeded (max %d per round)", MaxAttemptsPerRound)
	}
	for _, a := range r.Attempts {
		if a.State == ExecUnknown {
			return nil, fmt.Errorf("previous attempt is UNKNOWN: re-dispatch forbidden to avoid duplicate execution")
		}
		if a.State != ExecFailed && a.State != ExecCanceled {
			return nil, fmt.Errorf("previous attempt is %s: a new attempt requires a terminal failed/canceled prior attempt", a.State)
		}
	}
	a := &Attempt{Index: len(r.Attempts), State: ExecPrepared}
	r.Attempts = append(r.Attempts, a)
	return a, nil
}

// Transition moves an attempt's execution state under contract validation.
func (a *Attempt) Transition(to ExecutionState) error {
	if err := ValidateExecTransition(a.State, to); err != nil {
		return err
	}
	a.State = to
	return nil
}

// OpenConfirmation starts the round's single confirmation cycle.
func (r *Round) OpenConfirmation(notConfirmedIDs []string) (*ConfirmationCycle, error) {
	if r.Confirmation != nil {
		return nil, fmt.Errorf("confirmation cycle already exists for round R%d (max 1 per round)", r.Index)
	}
	r.Confirmation = &ConfirmationCycle{OutstandingIDs: notConfirmedIDs}
	return r.Confirmation, nil
}

// RecordPreconditionFailure notes a revision-mismatch or packet validation
// failure. It never consumes a valid attempt.
func (c *ConfirmationCycle) RecordPreconditionFailure() {
	c.PreconditionFailures++
}

// SubmitValidAttempt records one valid confirmation attempt over ids, which
// must be a subset of the outstanding not-confirmed IDs. confirmed lists the
// IDs the reviewer confirmed in this attempt.
func (c *ConfirmationCycle) SubmitValidAttempt(ids, confirmed []string) error {
	if c.Escalated {
		return fmt.Errorf("confirmation cycle escalated to owner gate: no further attempts")
	}
	if c.ValidAttempts >= MaxValidConfirmationRetries {
		return fmt.Errorf("confirmation attempt bound exceeded (max %d): owner gate required", MaxValidConfirmationRetries)
	}
	if c.ValidAttempts > 0 { // resubmission: subset of outstanding only
		outstanding := map[string]bool{}
		for _, id := range c.OutstandingIDs {
			outstanding[id] = true
		}
		for _, id := range ids {
			if !outstanding[id] {
				return fmt.Errorf("resubmission may only contain not-confirmed IDs: %q is not outstanding", id)
			}
		}
	}
	c.ValidAttempts++
	conf := map[string]bool{}
	for _, id := range confirmed {
		conf[id] = true
	}
	var remaining []string
	for _, id := range ids {
		if !conf[id] {
			remaining = append(remaining, id)
		}
	}
	c.OutstandingIDs = remaining
	if len(remaining) > 0 && c.ValidAttempts >= MaxValidConfirmationRetries {
		c.Escalated = true
	}
	return nil
}

// ValidateSameTargetLink enforces the prior-objective rule: a new objective
// on an already-reviewed target manifest must carry a prior pointer and a
// material-difference statement. Presence only — meaning is not judged.
func (o *Objective) ValidateSameTargetLink(targetSeenBefore bool) error {
	if !targetSeenBefore {
		return nil
	}
	if o.PriorObjective == "" || o.MaterialDifference == "" {
		return fmt.Errorf("same-target objective requires prior objective pointer and material-difference reason: fail-closed")
	}
	return nil
}
