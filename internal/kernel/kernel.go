package kernel

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
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
// The governance state is unexported: every change goes through a method
// that enforces the contract atomically (R0-CX-F2) — there is no way to set
// a terminal state without its required evidence.
type Objective struct {
	ID              string
	CollaborationID string
	Question        string
	TargetRevision  string // pre-dispatch snapshot of the target manifest
	Rounds          []*Round
	// PriorObjective and MaterialDifference are mandatory when this
	// objective references the same target manifest as an earlier one.
	// The relay validates presence only, never meaning.
	PriorObjective     string
	MaterialDifference string

	governance GovernanceState
	terminal   *TerminalReason
}

// NewObjective is the only way to obtain a well-formed objective; the zero
// value has an unknown governance state and every method fails closed on it.
func NewObjective(id, collaborationID, question, targetRevision string) *Objective {
	return &Objective{
		ID: id, CollaborationID: collaborationID,
		Question: question, TargetRevision: targetRevision,
		governance: GovOpen,
	}
}

// Governance returns the current governance state.
func (o *Objective) Governance() GovernanceState { return o.governance }

// Terminal returns the recorded terminal reason, if any.
func (o *Objective) Terminal() *TerminalReason { return o.terminal }

func (o *Objective) transition(to GovernanceState) error {
	if err := ValidateGovTransition(o.governance, to); err != nil {
		return err
	}
	o.governance = to
	return nil
}

// RequireDecision moves the objective into DECISION_REQUIRED.
func (o *Objective) RequireDecision() error { return o.transition(GovDecisionRequired) }

// MarkClosable moves the objective into CLOSABLE.
func (o *Objective) MarkClosable() error { return o.transition(GovClosable) }

// Close ends the objective successfully. It is only reachable from
// CLOSABLE and only when the supplied closure check (the review profile's
// fail-closed gate) passes. There is no other path to CLOSED.
func (o *Objective) Close(closureCheck func() error) error {
	if o.governance != GovClosable {
		return fmt.Errorf("close from %s refused: objective must be CLOSABLE", o.governance)
	}
	if closureCheck == nil {
		return fmt.Errorf("close without closure check refused: fail-closed")
	}
	if err := closureCheck(); err != nil {
		return fmt.Errorf("closure check failed: %w", err)
	}
	o.governance = GovClosed
	return nil
}

// Terminate ends the objective without closure (SUPERSEDED or ABANDONED).
// The arbiter identity and reason are validated and recorded in the same
// step — a terminal state can never exist without them (R0-CX-F2).
func (o *Objective) Terminate(to GovernanceState, reason TerminalReason) error {
	if to != GovSuperseded && to != GovAbandoned {
		return fmt.Errorf("terminate to %s refused: only SUPERSEDED/ABANDONED", to)
	}
	if err := reason.Validate(); err != nil {
		return err
	}
	if err := ValidateGovTransition(o.governance, to); err != nil {
		return err
	}
	o.governance = to
	o.terminal = &reason
	return nil
}

// Round is one formal reviewer assessment (R0..R2).
type Round struct {
	Index    int // 0-based
	Attempts []*Attempt
	// Confirmation is the at-most-one confirmation cycle for this round.
	Confirmation *ConfirmationCycle
}

// Attempt is one transport/adapter invocation. A prepared attempt is not
// part of any round until CommitDispatch: preflight failures therefore
// never consume the attempt budget (R0-CX-F3).
type Attempt struct {
	Index int
	State ExecutionState
}

// NewID returns a cryptographically random identifier with the given prefix.
// Identifiers are never derived from vendor handles or other stable IDs
// (DR-811 §3).
func NewID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(b), nil
}

var knownNonTerminal = map[GovernanceState]bool{
	GovOpen: true, GovDecisionRequired: true, GovClosable: true,
}

// OpenRound starts the next formal round, enforcing the R0..R2 bound.
// Unknown governance states (including the zero value) fail closed.
func (o *Objective) OpenRound() (*Round, error) {
	if !knownNonTerminal[o.governance] {
		return nil, fmt.Errorf("objective %s governance %q does not admit rounds: fail-closed", o.ID, o.governance)
	}
	if len(o.Rounds) >= MaxRoundsPerObjective {
		return nil, fmt.Errorf("round bound exceeded (max R0..R%d): owner decision gate required", MaxRoundsPerObjective-1)
	}
	r := &Round{Index: len(o.Rounds)}
	o.Rounds = append(o.Rounds, r)
	return r, nil
}

// PrepareAttempt creates an attempt outside the round budget. Preflight and
// revision snapshots happen against this prepared attempt.
func (r *Round) PrepareAttempt() *Attempt {
	return &Attempt{State: ExecPrepared}
}

// CommitDispatch admits a prepared attempt into the round at the moment the
// child process is about to start. Only committed attempts count against
// the per-round bound; an UNKNOWN prior attempt forbids any new commit.
func (r *Round) CommitDispatch(a *Attempt) error {
	if a == nil || a.State != ExecPrepared {
		return fmt.Errorf("only a PREPARED attempt can be committed")
	}
	if len(r.Attempts) >= MaxAttemptsPerRound {
		return fmt.Errorf("attempt bound exceeded (max %d per round)", MaxAttemptsPerRound)
	}
	for _, prev := range r.Attempts {
		if prev.State == ExecUnknown {
			return fmt.Errorf("previous attempt is UNKNOWN: re-dispatch forbidden to avoid duplicate execution")
		}
		if prev.State != ExecFailed && prev.State != ExecCanceled {
			return fmt.Errorf("previous attempt is %s: a new attempt requires a terminal failed/canceled prior attempt", prev.State)
		}
	}
	if err := a.Transition(ExecDispatched); err != nil {
		return err
	}
	a.Index = len(r.Attempts)
	r.Attempts = append(r.Attempts, a)
	return nil
}

// Transition moves an attempt's execution state under contract validation.
func (a *Attempt) Transition(to ExecutionState) error {
	if err := ValidateExecTransition(a.State, to); err != nil {
		return err
	}
	a.State = to
	return nil
}

// ConfirmationCycle implements the confirmation contract (DR-811 §6 +
// R0-CX-F1 hardening): the initial closed set is immutable; submissions and
// confirmations are validated as unique nonblank subsets; unsubmitted
// findings always remain outstanding; the cycle completes only when nothing
// is outstanding.
type ConfirmationCycle struct {
	initial              map[string]bool
	outstanding          map[string]bool
	ValidAttempts        int
	PreconditionFailures int
	Escalated            bool
}

func validateIDSet(ids []string) (map[string]bool, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("ID list must be nonempty")
	}
	set := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("blank ID refused")
		}
		if set[id] {
			return nil, fmt.Errorf("duplicate ID %q refused", id)
		}
		set[id] = true
	}
	return set, nil
}

// OpenConfirmation starts the round's single confirmation cycle over the
// closed finding IDs.
func (r *Round) OpenConfirmation(notConfirmedIDs []string) (*ConfirmationCycle, error) {
	if r.Confirmation != nil {
		return nil, fmt.Errorf("confirmation cycle already exists for round R%d (max 1 per round)", r.Index)
	}
	set, err := validateIDSet(notConfirmedIDs)
	if err != nil {
		return nil, fmt.Errorf("confirmation open: %w", err)
	}
	outstanding := map[string]bool{}
	initial := map[string]bool{}
	for id := range set {
		outstanding[id] = true
		initial[id] = true
	}
	r.Confirmation = &ConfirmationCycle{initial: initial, outstanding: outstanding}
	return r.Confirmation, nil
}

// Outstanding returns the sorted not-confirmed IDs.
func (c *ConfirmationCycle) Outstanding() []string {
	var out []string
	for id := range c.outstanding {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Done reports whether every finding in the cycle has been confirmed.
func (c *ConfirmationCycle) Done() bool { return len(c.outstanding) == 0 }

// RecordPreconditionFailure notes a revision-mismatch or packet validation
// failure. It never consumes a valid attempt.
func (c *ConfirmationCycle) RecordPreconditionFailure() {
	c.PreconditionFailures++
}

// SubmitValidAttempt records one valid confirmation attempt. ids must be a
// unique nonblank subset of the current outstanding set; confirmed must be
// a subset of ids. Findings not submitted stay outstanding.
func (c *ConfirmationCycle) SubmitValidAttempt(ids, confirmed []string) error {
	if c.Escalated {
		return fmt.Errorf("confirmation cycle escalated to owner gate: no further attempts")
	}
	if c.Done() {
		return fmt.Errorf("confirmation cycle already complete")
	}
	if c.ValidAttempts >= MaxValidConfirmationRetries {
		return fmt.Errorf("confirmation attempt bound exceeded (max %d): owner gate required", MaxValidConfirmationRetries)
	}
	idSet, err := validateIDSet(ids)
	if err != nil {
		return fmt.Errorf("submission: %w", err)
	}
	for id := range idSet {
		if !c.outstanding[id] {
			return fmt.Errorf("submission may only contain outstanding not-confirmed IDs: %q is not outstanding", id)
		}
	}
	confSet := map[string]bool{}
	if len(confirmed) > 0 {
		confSet, err = validateIDSet(confirmed)
		if err != nil {
			return fmt.Errorf("confirmed set: %w", err)
		}
		for id := range confSet {
			if !idSet[id] {
				return fmt.Errorf("confirmed ID %q was not part of this submission: fail-closed", id)
			}
		}
	}
	c.ValidAttempts++
	for id := range confSet {
		delete(c.outstanding, id)
	}
	if !c.Done() && c.ValidAttempts >= MaxValidConfirmationRetries {
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
