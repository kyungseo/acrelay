// Package relay wires the kernel, review profile, store, and adapters into
// the one-shot review flow. The wiring order follows R0-CX-F3:
// preflight → revision snapshot → attempt commit → dispatch → validate →
// append. State lives inside the canonical Markdown as relay-managed,
// sequence-numbered JSON blocks — the canonical document stays the single
// writable artifact (DR-811 §2).
package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/store"
)

// ReviewSchema is the canonical ReviewResult JSON schema sent to every
// reviewer (DR-811 §5).
const ReviewSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["approve","changes-requested"]},"findings":{"type":"array","items":{"type":"string"}}},"required":["verdict","findings"],"additionalProperties":false}`

// State is the machine-readable snapshot appended after every mutation.
// The highest sequence number wins; history stays in the document.
type State struct {
	Seq             int              `json:"seq"`
	CollaborationID string           `json:"collaboration_id"`
	ObjectiveID     string           `json:"objective_id"`
	Question        string           `json:"question"`
	TargetRevision  string           `json:"target_revision"`
	Governance      string           `json:"governance"`
	TerminalArbiter string           `json:"terminal_arbiter,omitempty"`
	TerminalReason  string           `json:"terminal_reason,omitempty"`
	SessionRef      string           `json:"session_ref,omitempty"`
	Vendor          string           `json:"vendor,omitempty"`
	Rounds          []RoundState     `json:"rounds"`
	Findings        []review.Finding `json:"findings"`
	PriorObjective  string           `json:"prior_objective,omitempty"`
	MaterialDiff    string           `json:"material_difference,omitempty"`
}

// RoundState mirrors one committed round.
type RoundState struct {
	Index    int      `json:"index"`
	Attempts []string `json:"attempts"` // execution states in commit order
	Verdict  string   `json:"verdict,omitempty"`
	Outcome  string   `json:"outcome,omitempty"`
}

// Session binds one adapter+handle store to one canonical document.
type Session struct {
	Adapter   adapter.Adapter
	Handles   *adapter.HandleStore
	Canonical string
}

var stateHead = regexp.MustCompile(`- acrelay_state_(\d+): encoding=utf-8`)

// LoadState returns the highest-sequence state block, or nil when the
// canonical has no state yet.
func LoadState(canonical string) (*State, error) {
	doc, err := store.ReadAll(canonical)
	if err != nil {
		return nil, err
	}
	if doc == "" {
		return nil, nil
	}
	matches := stateHead.FindAllStringSubmatch(doc, -1)
	if len(matches) == 0 {
		return nil, nil
	}
	max := -1
	for _, m := range matches {
		n, _ := strconv.Atoi(m[1])
		if n > max {
			max = n
		}
	}
	raw, err := store.ExtractBlock(doc, fmt.Sprintf("acrelay_state_%d", max))
	if err != nil {
		return nil, fmt.Errorf("state block %d unreadable: %w", max, err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("state block %d corrupt: fail-closed: %w", max, err)
	}
	return &st, nil
}

func stateSection(st *State) (string, error) {
	st.Seq++
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return "", err
	}
	return store.EncodeBlock(fmt.Sprintf("acrelay_state_%d", st.Seq), b), nil
}

// rehydrate rebuilds a kernel objective from persisted state by replaying
// contract methods — never by direct field assignment.
func rehydrate(st *State) (*kernel.Objective, error) {
	o := kernel.NewObjective(st.ObjectiveID, st.CollaborationID, st.Question, st.TargetRevision)
	o.PriorObjective, o.MaterialDifference = st.PriorObjective, st.MaterialDiff
	for _, rs := range st.Rounds {
		r, err := o.OpenRound()
		if err != nil {
			return nil, fmt.Errorf("rehydrate round %d: %w", rs.Index, err)
		}
		for _, as := range rs.Attempts {
			a := r.PrepareAttempt()
			if err := r.CommitDispatch(a); err != nil {
				return nil, fmt.Errorf("rehydrate attempt: %w", err)
			}
			for _, step := range replaySteps(kernel.ExecutionState(as)) {
				if err := a.Transition(step); err != nil {
					return nil, fmt.Errorf("rehydrate attempt state %s: %w", as, err)
				}
			}
		}
	}
	switch kernel.GovernanceState(st.Governance) {
	case kernel.GovOpen:
	case kernel.GovDecisionRequired:
		if err := o.RequireDecision(); err != nil {
			return nil, err
		}
	case kernel.GovClosable:
		if err := o.MarkClosable(); err != nil {
			return nil, err
		}
	case kernel.GovClosed:
		if err := o.MarkClosable(); err != nil {
			return nil, err
		}
		if err := o.Close(func() error { return review.ClosureCheck(st.Findings) }); err != nil {
			return nil, err
		}
	case kernel.GovSuperseded, kernel.GovAbandoned:
		if err := o.Terminate(kernel.GovernanceState(st.Governance),
			kernel.TerminalReason{Arbiter: st.TerminalArbiter, Reason: st.TerminalReason}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown persisted governance %q: fail-closed", st.Governance)
	}
	return o, nil
}

func replaySteps(final kernel.ExecutionState) []kernel.ExecutionState {
	switch final {
	case kernel.ExecDispatched:
		return nil
	case kernel.ExecRunning:
		return []kernel.ExecutionState{kernel.ExecRunning}
	case kernel.ExecSucceeded, kernel.ExecFailed, kernel.ExecCanceled, kernel.ExecUnknown:
		if final == kernel.ExecFailed {
			// dispatch-level failure may have skipped RUNNING
			return []kernel.ExecutionState{kernel.ExecFailed}
		}
		return []kernel.ExecutionState{kernel.ExecRunning, final}
	}
	return []kernel.ExecutionState{final}
}

// Init creates the collaboration/objective and writes the first canonical
// section. A same-target objective must carry the prior pointer.
func Init(canonical, question, targetRevision, priorObjective, materialDiff string, targetSeenBefore bool) (*State, error) {
	prev, err := LoadState(canonical)
	if err != nil {
		return nil, err
	}
	if prev != nil && !kernel.IsGovTerminal(kernel.GovernanceState(prev.Governance)) {
		return nil, fmt.Errorf("canonical already tracks objective %s in state %s: close or terminate it first", prev.ObjectiveID, prev.Governance)
	}
	collabID, err := kernel.NewID("collab")
	if err != nil {
		return nil, err
	}
	objID, err := kernel.NewID("obj")
	if err != nil {
		return nil, err
	}
	carrySessionRef, carryVendor, carrySeq := "", "", 0
	if prev != nil {
		collabID = prev.CollaborationID // same collaboration continues across objectives
		// DR-811 §1: related objectives keep the same reviewer session.
		carrySessionRef, carryVendor = prev.SessionRef, prev.Vendor
		// the state sequence is monotonic across the whole collaboration —
		// a restarted sequence would resurrect the previous objective's
		// state as "latest" (defect caught by the objective-transition fixture).
		carrySeq = prev.Seq
	}
	o := kernel.NewObjective(objID, collabID, question, targetRevision)
	o.PriorObjective, o.MaterialDifference = priorObjective, materialDiff
	if err := o.ValidateSameTargetLink(targetSeenBefore); err != nil {
		return nil, err
	}
	st := &State{
		Seq:             carrySeq,
		CollaborationID: collabID, ObjectiveID: objID, Question: question,
		TargetRevision: targetRevision, Governance: string(kernel.GovOpen),
		PriorObjective: priorObjective, MaterialDiff: materialDiff,
		SessionRef: carrySessionRef, Vendor: carryVendor,
	}
	rev, err := store.Revision(canonical)
	if err != nil {
		return nil, err
	}
	block, err := stateSection(st)
	if err != nil {
		return nil, err
	}
	section := fmt.Sprintf("\n## objective %s\n- collaboration: %s\n- question: %s\n- target_revision: %s\n%s",
		objID, collabID, question, targetRevision, block)
	if _, err := store.AppendAtomic(canonical, section, rev); err != nil {
		return nil, err
	}
	return st, nil
}

// Review runs one formal round: snapshot → commit → dispatch → validate →
// append. It returns the updated state and the dispatch outcome.
func (s *Session) Review(ctx context.Context, prompt string, req adapter.Request) (*State, review.Outcome, error) {
	st, err := LoadState(s.Canonical)
	if err != nil {
		return nil, "", err
	}
	if st == nil {
		return nil, "", fmt.Errorf("canonical %s has no objective: run init first", s.Canonical)
	}
	o, err := rehydrate(st)
	if err != nil {
		return nil, "", err
	}
	// session continuity: reuse the stored session_ref unless the caller
	// explicitly overrides (never silently fall back to new).
	if req.ResumeRef == "" && st.SessionRef != "" && st.Vendor == s.Adapter.Vendor() {
		req.ResumeRef = st.SessionRef
	}
	req.Prompt = prompt
	req.SchemaJSON = ReviewSchema

	round, err := o.OpenRound()
	if err != nil {
		return nil, "", err
	}
	attempt := round.PrepareAttempt()
	// 1. non-consuming preflight
	if err := s.Adapter.PreDispatch(ctx, req, s.Handles); err != nil {
		_ = attempt.Transition(kernel.ExecFailed) // prepared-only, never committed
		return nil, "", fmt.Errorf("pre-dispatch failure (no round/attempt consumed): %w", err)
	}
	// 2. pre-dispatch canonical revision snapshot
	snapshot, err := store.Revision(s.Canonical)
	if err != nil {
		return nil, "", err
	}
	// 3. commit the attempt at the dispatch boundary
	if err := round.CommitDispatch(attempt); err != nil {
		return nil, "", err
	}
	// 4. dispatch
	res, dispatchErr := s.Adapter.Dispatch(ctx, req, s.Handles)
	var outcome review.Outcome
	switch {
	case res != nil && res.TimedOut:
		_ = attempt.Transition(kernel.ExecRunning)
		_ = attempt.Transition(kernel.ExecUnknown)
		outcome = review.OutcomeFailed
	case dispatchErr != nil:
		_ = attempt.Transition(kernel.ExecRunning)
		_ = attempt.Transition(kernel.ExecFailed)
		outcome = review.OutcomeFailed
	default:
		_ = attempt.Transition(kernel.ExecRunning)
		_ = attempt.Transition(kernel.ExecSucceeded)
	}

	verdict := ""
	var newFindings []review.Finding
	if attempt.State == kernel.ExecSucceeded {
		vErrs := review.ValidateResult(res.Structured)
		for _, note := range res.Invalid {
			vErrs = append(vErrs, note)
		}
		outcome = review.ClassifyOutcome(kernel.ExecSucceeded, vErrs)
		if outcome == review.OutcomeResultValid {
			verdict = res.Structured["verdict"].(string)
			blocking := verdict == string(review.VerdictChangesRequested)
			for i, f := range res.Structured["findings"].([]any) {
				newFindings = append(newFindings, review.Finding{
					ID:       fmt.Sprintf("R%d-F%d", round.Index, i+1),
					Severity: "reviewer-reported", Blocking: blocking,
					Summary: f.(string),
				})
			}
		}
	}

	// 5. append raw evidence + updated state under the pre-dispatch snapshot
	if res != nil && res.Provenance.SessionRef != "" {
		st.SessionRef, st.Vendor = res.Provenance.SessionRef, s.Adapter.Vendor()
	}
	st.Findings = append(st.Findings, newFindings...)
	st.Rounds = append(st.Rounds, RoundState{
		Index: round.Index, Attempts: []string{string(attempt.State)},
		Verdict: verdict, Outcome: string(outcome),
	})
	switch {
	case outcome == review.OutcomeResultValid && verdict == string(review.VerdictApprove) && review.ClosureCheck(st.Findings) == nil:
		st.Governance = string(kernel.GovClosable)
	default:
		st.Governance = string(kernel.GovDecisionRequired)
	}

	label := fmt.Sprintf("r%da%d", round.Index, attempt.Index)
	section := fmt.Sprintf("\n## round R%d attempt A%d\n- outcome: %s\n- verdict: %s\n", round.Index, attempt.Index, outcome, verdict)
	if res != nil {
		prov, _ := json.Marshal(res.Provenance)
		section += fmt.Sprintf("- provenance: %s\n- diagnostic: %q\n", prov, res.Diagnostic)
		section += store.EncodeBlock("raw_stdout "+label, res.Stdout)
		section += store.EncodeBlock("raw_stderr "+label, res.Stderr)
	}
	if dispatchErr != nil {
		section += fmt.Sprintf("- dispatch_error: %q\n", dispatchErr.Error())
	}
	block, err := stateSection(st)
	if err != nil {
		return nil, "", err
	}
	section += block
	if _, err := store.AppendAtomic(s.Canonical, section, snapshot); err != nil {
		return nil, "", fmt.Errorf("append after dispatch: %w (raw preserved in memory only — record manually)", err)
	}
	return st, outcome, nil
}

// Disposition records the driver's response to a finding and persists it.
func Disposition(canonical, findingID string, d review.Disposition, decision *review.ArbiterDecision) (*State, error) {
	if !review.ValidDisposition(d) {
		return nil, fmt.Errorf("invalid disposition %q", d)
	}
	st, err := LoadState(canonical)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("no objective in canonical")
	}
	found := false
	for i := range st.Findings {
		if st.Findings[i].ID == findingID {
			st.Findings[i].Disposition = d
			st.Findings[i].Decision = decision
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("finding %s not found", findingID)
	}
	if review.ClosureCheck(st.Findings) == nil && st.Governance == string(kernel.GovDecisionRequired) {
		st.Governance = string(kernel.GovClosable)
	}
	return st, appendState(canonical, st, fmt.Sprintf("\n## disposition %s\n- decision: %s\n", findingID, d))
}

// Close ends the objective through the fail-closed gate.
func Close(canonical string) (*State, error) {
	st, err := LoadState(canonical)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("no objective in canonical")
	}
	o, err := rehydrate(st)
	if err != nil {
		return nil, err
	}
	if o.Governance() == kernel.GovDecisionRequired || o.Governance() == kernel.GovOpen {
		if err := review.ClosureCheck(st.Findings); err != nil {
			return nil, err
		}
		if o.Governance() != kernel.GovClosable {
			if err := o.MarkClosable(); err != nil {
				return nil, err
			}
		}
	}
	if err := o.Close(func() error { return review.ClosureCheck(st.Findings) }); err != nil {
		return nil, err
	}
	st.Governance = string(kernel.GovClosed)
	return st, appendState(canonical, st, "\n## closure\n- result: CLOSED\n")
}

// Terminate ends the objective as SUPERSEDED or ABANDONED with arbiter
// identity and reason.
func Terminate(canonical string, to kernel.GovernanceState, arbiter, reason string) (*State, error) {
	st, err := LoadState(canonical)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("no objective in canonical")
	}
	o, err := rehydrate(st)
	if err != nil {
		return nil, err
	}
	if err := o.Terminate(to, kernel.TerminalReason{Arbiter: arbiter, Reason: reason}); err != nil {
		return nil, err
	}
	st.Governance = string(to)
	st.TerminalArbiter, st.TerminalReason = arbiter, reason
	return st, appendState(canonical, st, fmt.Sprintf("\n## terminal\n- result: %s\n- arbiter: %s\n- reason: %s\n", to, arbiter, reason))
}

func appendState(canonical string, st *State, header string) error {
	rev, err := store.Revision(canonical)
	if err != nil {
		return err
	}
	block, err := stateSection(st)
	if err != nil {
		return err
	}
	_, err = store.AppendAtomic(canonical, header+block, rev)
	return err
}

// Status renders a short human-readable summary.
func Status(canonical string) (string, error) {
	st, err := LoadState(canonical)
	if err != nil {
		return "", err
	}
	if st == nil {
		return "no objective", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "objective %s (%s)\ngovernance: %s\nrounds: %d/%d\n",
		st.ObjectiveID, st.Question, st.Governance, len(st.Rounds), kernel.MaxRoundsPerObjective)
	var open []string
	for _, f := range st.Findings {
		if f.Blocking && f.Disposition == "" {
			open = append(open, f.ID)
		}
	}
	sort.Strings(open)
	if len(open) > 0 {
		fmt.Fprintf(&b, "undispositioned blocking findings: %s\n", strings.Join(open, ", "))
	}
	return b.String(), nil
}
