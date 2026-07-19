// Package relay wires the kernel, review profile, store, and adapters into
// the one-shot review flow. The wiring order follows R0-CX-F3:
// preflight → revision snapshots (canonical AND target) → attempt commit →
// dispatch → validate → append. State lives inside the canonical Markdown as
// relay-managed, sequence-numbered blocks parsed fence-aware (R1-CX-F2) —
// the canonical document stays the single writable artifact (DR-811 §2).
package relay

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/store"
)

// ReviewSchema is the canonical ReviewResult JSON schema sent to every
// reviewer (DR-811 §5).
const ReviewSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["approve","changes-requested"]},"findings":{"type":"array","items":{"type":"string"}}},"required":["verdict","findings"],"additionalProperties":false}`

// Format versions (DR-811 §8). Unknown persisted versions fail closed.
const (
	KernelVersion  = "kernel v0.1"
	ProfileVersion = "review-profile v0.1"
	StoreVersion   = "store-md v0.1"
)

// State is the machine-readable snapshot appended after every mutation.
// The highest sequence number wins; history stays in the document.
type State struct {
	Seq             int    `json:"seq"`
	KernelVersion   string `json:"kernel_version"`
	ProfileVersion  string `json:"profile_version"`
	StoreVersion    string `json:"store_version"`
	CollaborationID string `json:"collaboration_id"`
	ObjectiveID     string `json:"objective_id"`
	Question        string `json:"question"`
	// Target is an evidence pointer: location + raw-byte digest (R1-CX-F3).
	TargetLocation  string           `json:"target_location"`
	TargetRevision  string           `json:"target_revision"`
	TargetResolved  string           `json:"target_resolved,omitempty"` // symlink resolution fact
	Governance      string           `json:"governance"`
	TerminalArbiter string           `json:"terminal_arbiter,omitempty"`
	TerminalReason  string           `json:"terminal_reason,omitempty"`
	SessionRef      string           `json:"session_ref,omitempty"`
	Vendor          string           `json:"vendor,omitempty"`
	SessionChanges  []SessionChange  `json:"session_changes,omitempty"`
	Rounds          []RoundState     `json:"rounds"`
	Confirmations   []ConfState      `json:"confirmations,omitempty"`
	Findings        []review.Finding `json:"findings"`
	PriorObjective  string           `json:"prior_objective,omitempty"`
	MaterialDiff    string           `json:"material_difference,omitempty"`
	Advances        []AdvanceState   `json:"advances,omitempty"`
}

// RoundState mirrors one committed round.
type RoundState struct {
	Index    int      `json:"index"`
	Attempts []string `json:"attempts"`
	Verdict  string   `json:"verdict,omitempty"`
	Outcome  string   `json:"outcome,omitempty"`
	Stale    bool     `json:"stale,omitempty"`    // target changed mid-dispatch
	Revision string   `json:"revision,omitempty"` // target revision this round reviewed (Gate A-3)
}

// AdvanceState records one authorized target-revision advancement inside the
// objective (Gate A-1): the explicit continuation of the
// review→revise→re-review loop. Prior rounds keep their reviewed revision;
// the linkage is AfterRound.
type AdvanceState struct {
	FromRevision string `json:"from_revision"`
	ToRevision   string `json:"to_revision"`
	AfterRound   int    `json:"after_round"`
	Note         string `json:"note,omitempty"`
}

// ConfState persists one round's confirmation cycle (R1-CX-F4).
type ConfState struct {
	RoundIndex           int      `json:"round_index"`
	Initial              []string `json:"initial"`
	Outstanding          []string `json:"outstanding"`
	ValidAttempts        int      `json:"valid_attempts"`
	PreconditionFailures int      `json:"precondition_failures"`
	Escalated            bool     `json:"escalated"`
}

// SessionChange records an explicit reviewer-session reset (R1-CX-F6).
type SessionChange struct {
	FromRef  string `json:"from_ref"`
	ToVendor string `json:"to_vendor"`
	Mode     string `json:"mode"` // second-opinion | context-reset | resume-failure | unrelated
	Reason   string `json:"reason"`
}

// SessionReset authorizes a reviewer/session change. Without it a vendor
// switch on an existing session fails closed — never a silent new session.
type SessionReset struct {
	Mode   string
	Reason string
}

var validResetModes = map[string]bool{
	"second-opinion": true, "context-reset": true, "resume-failure": true, "unrelated": true,
}

// Session binds one adapter+handle store to one canonical document.
type Session struct {
	Adapter   adapter.Adapter
	Handles   *adapter.HandleStore
	Canonical string
	Reset     *SessionReset // required to switch vendor/session
}

// TargetSnapshot computes the evidence-pointer digest of the target's raw
// bytes, recording symlink resolution as a fact.
func TargetSnapshot(location string) (digest, resolved string, err error) {
	resolved = location
	if r, lerr := filepath.EvalSymlinks(location); lerr == nil {
		resolved = r
	}
	b, err := os.ReadFile(resolved)
	if err != nil {
		return "", "", fmt.Errorf("target %s unreadable: %w", location, err)
	}
	return store.Digest(b), resolved, nil
}

const statePrefix = "acrelay_state_"

// LoadState returns the highest-sequence state block. Parsing is
// fence-aware: forged headers inside raw evidence are invisible (R1-CX-F2).
func LoadState(canonical string) (*State, error) {
	doc, err := store.ReadAll(canonical)
	if err != nil {
		return nil, err
	}
	if doc == "" {
		return nil, nil
	}
	blocks, err := store.ListBlocks(doc)
	if err != nil {
		return nil, err
	}
	maxSeq, found := -1, false
	for _, b := range blocks {
		if !strings.HasPrefix(b.Label, statePrefix) {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(b.Label, statePrefix))
		if err != nil {
			return nil, fmt.Errorf("state label %q malformed: fail-closed", b.Label)
		}
		if n > maxSeq {
			maxSeq, found = n, true
		}
	}
	if !found {
		return nil, nil
	}
	raw, err := store.ExtractBlock(doc, fmt.Sprintf("%s%d", statePrefix, maxSeq))
	if err != nil {
		return nil, fmt.Errorf("state block %d unreadable: %w", maxSeq, err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("state block %d corrupt: fail-closed: %w", maxSeq, err)
	}
	return &st, validateState(&st, maxSeq)
}

// validateState enforces persisted-state integrity (R1-CX-F2).
func validateState(st *State, labelSeq int) error {
	if st.Seq != labelSeq {
		return fmt.Errorf("state seq %d does not match block label %d: fail-closed", st.Seq, labelSeq)
	}
	if st.KernelVersion != KernelVersion || st.ProfileVersion != ProfileVersion || st.StoreVersion != StoreVersion {
		return fmt.Errorf("state format versions %q/%q/%q not supported (want %q/%q/%q): fail-closed",
			st.KernelVersion, st.ProfileVersion, st.StoreVersion, KernelVersion, ProfileVersion, StoreVersion)
	}
	for i, r := range st.Rounds {
		if r.Index != i {
			return fmt.Errorf("round index %d at position %d breaks continuity: fail-closed", r.Index, i)
		}
	}
	if !hex64.MatchString(st.TargetRevision) {
		return fmt.Errorf("target revision %q is not a sha256 hex digest: fail-closed", st.TargetRevision)
	}
	seenConf := map[int]bool{}
	for _, c := range st.Confirmations {
		if c.RoundIndex < 0 || c.RoundIndex >= len(st.Rounds) {
			return fmt.Errorf("confirmation for unknown round %d: fail-closed", c.RoundIndex)
		}
		if seenConf[c.RoundIndex] {
			return fmt.Errorf("duplicate confirmation cycle for round %d: fail-closed", c.RoundIndex)
		}
		seenConf[c.RoundIndex] = true
		if _, err := kernel.RehydrateConfirmation(c.Initial, c.Outstanding, c.ValidAttempts, c.PreconditionFailures, c.Escalated); err != nil {
			return fmt.Errorf("confirmation R%d invalid: %w", c.RoundIndex, err)
		}
		// contradictory combinations fail closed (CP-2 F2)
		if len(c.Outstanding) == 0 && c.ValidAttempts == 0 {
			return fmt.Errorf("confirmation R%d complete without any valid attempt: fail-closed", c.RoundIndex)
		}
		if c.Escalated {
			if len(c.Outstanding) == 0 {
				return fmt.Errorf("confirmation R%d escalated but nothing outstanding: fail-closed", c.RoundIndex)
			}
			if c.ValidAttempts != kernel.MaxValidConfirmationRetries {
				return fmt.Errorf("confirmation R%d escalated at %d valid attempts (contract escalates only at %d): fail-closed", c.RoundIndex, c.ValidAttempts, kernel.MaxValidConfirmationRetries)
			}
		} else if c.ValidAttempts >= kernel.MaxValidConfirmationRetries && len(c.Outstanding) > 0 {
			return fmt.Errorf("confirmation R%d exhausted attempts without escalation flag: fail-closed", c.RoundIndex)
		}
	}
	return nil
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func stateSection(st *State) (string, error) {
	st.Seq++
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return "", err
	}
	return store.EncodeBlock(fmt.Sprintf("%s%d", statePrefix, st.Seq), b), nil
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
	case kernel.ExecFailed:
		return []kernel.ExecutionState{kernel.ExecFailed}
	case kernel.ExecSucceeded, kernel.ExecCanceled, kernel.ExecUnknown:
		return []kernel.ExecutionState{kernel.ExecRunning, final}
	}
	return []kernel.ExecutionState{final}
}

// Init creates the collaboration/objective and writes the first canonical
// section. The target is an evidence pointer (location + raw digest).
func Init(canonical, question, targetLocation, priorObjective, materialDiff string, targetSeenBefore bool) (*State, error) {
	prev, err := LoadState(canonical)
	if err != nil {
		return nil, err
	}
	if prev != nil && !kernel.IsGovTerminal(kernel.GovernanceState(prev.Governance)) {
		return nil, fmt.Errorf("canonical already tracks objective %s in state %s: close or terminate it first", prev.ObjectiveID, prev.Governance)
	}
	targetDigest, resolved, err := TargetSnapshot(targetLocation)
	if err != nil {
		return nil, err
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
		// monotonic across the collaboration — a restarted sequence would
		// resurrect the previous objective's state as "latest".
		carrySeq = prev.Seq
	}
	o := kernel.NewObjective(objID, collabID, question, targetDigest)
	o.PriorObjective, o.MaterialDifference = priorObjective, materialDiff
	if err := o.ValidateSameTargetLink(targetSeenBefore); err != nil {
		return nil, err
	}
	st := &State{
		Seq:            carrySeq,
		KernelVersion:  KernelVersion,
		ProfileVersion: ProfileVersion,
		StoreVersion:   StoreVersion,
		CollaborationID: collabID, ObjectiveID: objID, Question: question,
		TargetLocation: targetLocation, TargetRevision: targetDigest, TargetResolved: resolved,
		Governance:     string(kernel.GovOpen),
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
	section := fmt.Sprintf("\n## objective %s\n- collaboration: %s\n- question: %s\n- target: %s sha256=%s\n%s",
		objID, collabID, question, targetLocation, targetDigest, block)
	if _, err := store.AppendAtomic(canonical, section, rev); err != nil {
		return nil, err
	}
	return st, nil
}

// pendingRecoveries lists unreconciled recovery transactions (R1-CX-F5).
func pendingRecoveries(canonical string) ([]string, error) {
	return filepath.Glob(canonical + ".recovery-*")
}

// Review runs one formal round: snapshots → commit → dispatch → validate →
// append. It returns the updated state and the dispatch outcome.
func (s *Session) Review(ctx context.Context, prompt string, req adapter.Request) (*State, review.Outcome, error) {
	if recs, err := pendingRecoveries(s.Canonical); err != nil {
		return nil, "", err
	} else if len(recs) > 0 {
		return nil, "", fmt.Errorf("pending recovery transactions %v: reconcile before dispatching again (duplicate-execution guard)", recs)
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		return nil, "", err
	}
	if st == nil {
		return nil, "", fmt.Errorf("canonical %s has no objective: run init first", s.Canonical)
	}
	// Cross-round UNKNOWN guard (R0-F3): the kernel forbids re-dispatch inside
	// a round after an UNKNOWN attempt, but opening a *new* round would slip
	// past it. An UNKNOWN result (hard-cap/external kill) may have run to
	// completion server-side, so any further dispatch risks duplicate
	// execution until an owner resolves it — the arbiter terminates the
	// objective (SUPERSEDED/ABANDONED) and opens a follow-up. No automatic
	// retry (DR-811 §7).
	if r := unknownRound(st); r >= 0 {
		return nil, "", fmt.Errorf("round R%d ended UNKNOWN: re-dispatch forbidden until an owner resolves it (terminate the objective and open a follow-up) — no automatic retry", r)
	}
	o, err := rehydrate(st)
	if err != nil {
		return nil, "", err
	}
	// Session continuity (R1-CX-F6): an existing session binds the vendor.
	// Switching requires an explicit reset with mode+reason — never silent.
	sessionChanged := false
	if st.SessionRef != "" && s.Reset == nil {
		switch {
		case st.Vendor == s.Adapter.Vendor():
			if req.ResumeRef == "" {
				req.ResumeRef = st.SessionRef
			} else if req.ResumeRef != st.SessionRef {
				return nil, "", fmt.Errorf("explicit resume_ref differs from stored session %s: session reset with mode+reason required", st.SessionRef)
			}
		default:
			return nil, "", fmt.Errorf("stored reviewer session belongs to %s: switching to %s requires an explicit session reset (mode+reason) — silent new-session fallback is forbidden", st.Vendor, s.Adapter.Vendor())
		}
	}
	if s.Reset != nil {
		if !validResetModes[s.Reset.Mode] || strings.TrimSpace(s.Reset.Reason) == "" {
			return nil, "", fmt.Errorf("session reset requires a valid mode (second-opinion|context-reset|resume-failure|unrelated) and a reason")
		}
		st.SessionChanges = append(st.SessionChanges, SessionChange{
			FromRef: st.SessionRef, ToVendor: s.Adapter.Vendor(), Mode: s.Reset.Mode, Reason: s.Reset.Reason,
		})
		req.ResumeRef = "" // authorized new session; round counters are untouched
		sessionChanged = true
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
		_ = attempt.Transition(kernel.ExecFailed)
		return nil, "", fmt.Errorf("pre-dispatch failure (no round/attempt consumed): %w", err)
	}
	// 2. pre-dispatch snapshots: canonical AND target (R1-CX-F3)
	snapshot, err := store.Revision(s.Canonical)
	if err != nil {
		return nil, "", err
	}
	targetNow, _, err := TargetSnapshot(st.TargetLocation)
	if err != nil {
		return nil, "", fmt.Errorf("pre-dispatch target snapshot: %w", err)
	}
	if targetNow != st.TargetRevision {
		return nil, "", fmt.Errorf("target %s changed since objective init (stale): open a follow-up objective with a prior pointer", st.TargetLocation)
	}
	// 3. commit the attempt at the dispatch boundary
	if err := round.CommitDispatch(attempt); err != nil {
		return nil, "", err
	}
	// 4. dispatch
	res, dispatchErr := s.Adapter.Dispatch(ctx, req, s.Handles)
	// child never started → the attempt is NOT persisted (R1-CX-F7): the
	// in-memory commit is discarded with this rehydrated objective.
	if res != nil && !res.Started {
		return nil, "", fmt.Errorf("child start failure (no round/attempt persisted): %w", dispatchErr)
	}
	var outcome review.Outcome
	switch {
	case res != nil && res.TimedOut:
		_ = attempt.Transition(kernel.ExecRunning)
		// Timeout kind splits the execution state (DR-811 §7): startup/idle
		// expiry means no terminal output was ever captured → FAILED;
		// hard-cap expiry cut the invocation mid-flight → UNKNOWN.
		if res.TimeoutKind == adapter.TimeoutStartup || res.TimeoutKind == adapter.TimeoutIdle {
			_ = attempt.Transition(kernel.ExecFailed)
		} else {
			_ = attempt.Transition(kernel.ExecUnknown)
		}
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
		vErrs = append(vErrs, res.Invalid...)
		if res.Provenance.ModelMismatch {
			vErrs = append(vErrs, "model-mismatch") // observable mismatch never reaches result-valid (R1-CX-F7)
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
	// post-dispatch target re-check: change marks the result stale (DR-811)
	stale := false
	if after, _, terr := TargetSnapshot(st.TargetLocation); terr != nil || after != st.TargetRevision {
		stale = true
	}

	if res != nil && res.Provenance.SessionRef != "" {
		st.SessionRef, st.Vendor = res.Provenance.SessionRef, s.Adapter.Vendor()
	}
	st.Findings = append(st.Findings, newFindings...)
	st.Rounds = append(st.Rounds, RoundState{
		Index: round.Index, Attempts: []string{string(attempt.State)},
		Verdict: verdict, Outcome: string(outcome), Stale: stale,
		Revision: st.TargetRevision, // pre-dispatch check guaranteed disk == this
	})
	if !stale && outcome == review.OutcomeResultValid && verdict == string(review.VerdictApprove) && review.ClosureCheck(st.Findings) == nil {
		st.Governance = string(kernel.GovClosable)
	} else {
		st.Governance = string(kernel.GovDecisionRequired)
	}

	label := fmt.Sprintf("r%da%d", round.Index, attempt.Index)
	section := fmt.Sprintf("\n## round R%d attempt A%d\n- outcome: %s\n- verdict: %s\n- stale: %v\n", round.Index, attempt.Index, outcome, verdict, stale)
	if sessionChanged {
		section += fmt.Sprintf("- session_change: mode=%s reason=%q\n", s.Reset.Mode, s.Reset.Reason)
	}
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
		// R1-CX-F5: never lose the raw or the dispatch fact — persist an
		// owner-only recovery transaction and block further dispatches.
		rpath, rerr := writeRecovery(s.Canonical, section, st, snapshot)
		if rerr != nil {
			return nil, "", fmt.Errorf("append conflict AND recovery write failed: %v / %v", err, rerr)
		}
		return nil, "", fmt.Errorf("append conflict: %v — raw and dispatch fact preserved in recovery transaction %s (noncanonical, non-resumable); run reconcile before any further dispatch", err, rpath)
	}
	return st, outcome, nil
}

// recoveryPayload binds a recovery transaction to its canonical lineage
// (R1-CX-F5 CP): objective, collaboration, expected state sequence, the
// pre-dispatch snapshots, and the unappended section.
type recoveryPayload struct {
	Note            string `json:"note"`
	CollaborationID string `json:"collaboration_id"`
	ObjectiveID     string `json:"objective_id"`
	Seq             int    `json:"seq"` // seq of the state embedded in Section (current+1 at reconcile time)
	PreSnapshot     string `json:"pre_snapshot"`
	TargetRevision  string `json:"target_revision"`
	Section         string `json:"section"`
}

// writeRecovery persists the unappended section as an owner-only recovery
// transaction (noncanonical, non-resumable).
func writeRecovery(canonical, section string, st *State, preSnapshot string) (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	path := fmt.Sprintf("%s.recovery-%d-%s", canonical, os.Getpid(), hex.EncodeToString(suffix))
	payload, err := json.Marshal(recoveryPayload{
		Note:            "noncanonical recovery transaction — reconcile appends it to the canonical; it cannot be used for session resume or revision comparison",
		CollaborationID: st.CollaborationID, ObjectiveID: st.ObjectiveID, Seq: st.Seq,
		PreSnapshot: preSnapshot, TargetRevision: st.TargetRevision,
		Section: base64.StdEncoding.EncodeToString([]byte(section)),
	})
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(payload); err != nil {
		return "", err
	}
	return path, f.Sync()
}

// Reconcile appends a pending recovery transaction to the canonical under a
// fresh snapshot after verifying lineage: same collaboration/objective and
// the exact next state sequence. Foreign or replayed transactions fail
// closed (R1-CX-F5 CP).
func Reconcile(canonical, recoveryPath string) (*State, error) {
	b, err := os.ReadFile(recoveryPath)
	if err != nil {
		return nil, err
	}
	var payload recoveryPayload
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, fmt.Errorf("recovery transaction corrupt: fail-closed: %w", err)
	}
	section, err := base64.StdEncoding.DecodeString(payload.Section)
	if err != nil {
		return nil, fmt.Errorf("recovery transaction corrupt: fail-closed: %w", err)
	}
	cur, err := LoadState(canonical)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("reconcile refused: canonical has no state")
	}
	if payload.CollaborationID != cur.CollaborationID || payload.ObjectiveID != cur.ObjectiveID {
		return nil, fmt.Errorf("reconcile refused: recovery belongs to objective %s/%s, canonical tracks %s/%s: fail-closed",
			payload.CollaborationID, payload.ObjectiveID, cur.CollaborationID, cur.ObjectiveID)
	}
	if payload.Seq != cur.Seq+1 {
		return nil, fmt.Errorf("reconcile refused: recovery state seq %d does not follow current seq %d (replay or stale transaction): fail-closed", payload.Seq, cur.Seq)
	}
	if payload.TargetRevision != cur.TargetRevision {
		return nil, fmt.Errorf("reconcile refused: recovery target revision differs from canonical state: fail-closed")
	}
	if !hex64.MatchString(payload.PreSnapshot) {
		return nil, fmt.Errorf("reconcile refused: recovery pre-snapshot malformed: fail-closed")
	}
	// section ↔ lineage metadata 정합 (CP-2 F5): the embedded state block
	// must exist at exactly payload.Seq and describe the same lineage.
	secBlocks, err := store.ListBlocks(string(section))
	if err != nil {
		return nil, fmt.Errorf("reconcile refused: recovery section integrity: %w", err)
	}
	var embedded *State
	for _, b := range secBlocks {
		if b.Label == fmt.Sprintf("%s%d", statePrefix, payload.Seq) {
			raw, xerr := store.ExtractBlock(string(section), b.Label)
			if xerr != nil {
				return nil, fmt.Errorf("reconcile refused: embedded state unreadable: %w", xerr)
			}
			var es State
			if xerr := json.Unmarshal(raw, &es); xerr != nil {
				return nil, fmt.Errorf("reconcile refused: embedded state corrupt: %w", xerr)
			}
			embedded = &es
		}
	}
	if embedded == nil {
		return nil, fmt.Errorf("reconcile refused: recovery section lacks state block seq %d: fail-closed", payload.Seq)
	}
	if embedded.ObjectiveID != payload.ObjectiveID || embedded.CollaborationID != payload.CollaborationID ||
		embedded.TargetRevision != payload.TargetRevision || embedded.Seq != payload.Seq {
		return nil, fmt.Errorf("reconcile refused: recovery section state does not match lineage metadata: fail-closed")
	}
	rev, err := store.Revision(canonical)
	if err != nil {
		return nil, err
	}
	// PreSnapshot 비교: divergence 여부를 감사 가능하게 기록한다. 동일하면
	// canonical이 dispatch 이후 변하지 않았음을 뜻한다.
	note := fmt.Sprintf("\n## reconcile\n- recovery: %s\n- pre_snapshot: %s\n- current_revision: %s\n- diverged_since_dispatch: %v\n",
		filepath.Base(recoveryPath), payload.PreSnapshot, rev, rev != payload.PreSnapshot)
	if _, err := store.AppendAtomic(canonical, note+string(section), rev); err != nil {
		return nil, err
	}
	if err := os.Remove(recoveryPath); err != nil {
		return nil, err
	}
	return LoadState(canonical)
}

// OpenConfirmation starts the bounded confirmation cycle for a round.
func OpenConfirmation(canonical string, roundIndex int, ids []string) (*State, error) {
	var out *State
	err := withCanonicalLock(canonical, func() error {
		rev, err := store.Revision(canonical)
		if err != nil {
			return err
		}
		st, err := LoadState(canonical)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("no objective in canonical")
		}
		if roundIndex < 0 || roundIndex >= len(st.Rounds) {
			return fmt.Errorf("round R%d does not exist", roundIndex)
		}
		for _, c := range st.Confirmations {
			if c.RoundIndex == roundIndex {
				return fmt.Errorf("confirmation cycle already exists for round R%d (max 1 per round)", roundIndex)
			}
		}
		// confirmation은 driver가 disposition으로 닫은 finding에 대해서만 연다 (CP-2 F4)
		byID := map[string]review.Finding{}
		for _, f := range st.Findings {
			byID[f.ID] = f
		}
		for _, id := range ids {
			f, ok := byID[strings.TrimSpace(id)]
			if !ok {
				return fmt.Errorf("confirmation ID %q is not a known finding: fail-closed", id)
			}
			if f.Disposition == "" {
				return fmt.Errorf("finding %s has no disposition: only closed findings enter a confirmation cycle", id)
			}
		}
		r := &kernel.Round{Index: roundIndex}
		cyc, err := r.OpenConfirmation(ids)
		if err != nil {
			return err
		}
		st.Confirmations = append(st.Confirmations, ConfState{
			RoundIndex: roundIndex, Initial: cyc.Initial(), Outstanding: cyc.Outstanding(),
		})
		out = st
		return appendState(canonical, st, fmt.Sprintf("\n## confirmation open R%d\n- ids: %s\n", roundIndex, strings.Join(ids, ", ")), rev)
	})
	return out, err
}

// ConfirmSchema restricts confirmation output to per-ID statuses — no
// verdicts, no new findings (CP contract, R1-CX-F4).
const ConfirmSchema = `{"type":"object","properties":{"results":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"status":{"type":"string","enum":["confirmed","not-confirmed"]}},"required":["id","status"],"additionalProperties":false}}},"required":["results"],"additionalProperties":false}`

// parseConfirmResults validates reviewer confirmation output: every
// submitted ID exactly once, statuses from the enum, nothing else.
func parseConfirmResults(structured map[string]any, submitted []string) (confirmed []string, errs []string) {
	want := map[string]bool{}
	for _, id := range submitted {
		want[id] = true
	}
	for k := range structured {
		if k != "results" {
			errs = append(errs, "unknown-property:"+k)
		}
	}
	arr, ok := structured["results"].([]any)
	if !ok {
		return nil, append(errs, "results-not-array")
	}
	seen := map[string]bool{}
	for i, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("result-%d-not-object", i))
			continue
		}
		id, _ := m["id"].(string)
		status, _ := m["status"].(string)
		if !want[id] {
			errs = append(errs, "unsubmitted-id:"+id)
			continue
		}
		if seen[id] {
			errs = append(errs, "duplicate-id:"+id)
			continue
		}
		seen[id] = true
		switch status {
		case "confirmed":
			confirmed = append(confirmed, id)
		case "not-confirmed":
		default:
			errs = append(errs, "status-enum:"+status)
		}
	}
	for id := range want {
		if !seen[id] {
			errs = append(errs, "missing-id:"+id)
		}
	}
	return confirmed, errs
}

// ConfirmWithReviewer runs one confirmation attempt by dispatching the
// reviewer with the confirmation schema (R1-CX-F4 CP): the reviewer — not
// the operator — decides confirmed/not-confirmed. Precondition failures and
// invalid reviewer output consume no valid attempt. No formal round is
// consumed.
func (s *Session) ConfirmWithReviewer(ctx context.Context, roundIndex int, expectedTargetRev string, ids []string, claimedDelta string, req adapter.Request) (*State, bool, error) {
	loadRev, err := store.Revision(s.Canonical)
	if err != nil {
		return nil, false, err
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		return nil, false, err
	}
	if st == nil {
		return nil, false, fmt.Errorf("no objective in canonical")
	}
	var cs *ConfState
	for i := range st.Confirmations {
		if st.Confirmations[i].RoundIndex == roundIndex {
			cs = &st.Confirmations[i]
		}
	}
	if cs == nil {
		return nil, false, fmt.Errorf("no confirmation cycle for round R%d: open it first", roundIndex)
	}
	cyc, err := kernel.RehydrateConfirmation(cs.Initial, cs.Outstanding, cs.ValidAttempts, cs.PreconditionFailures, cs.Escalated)
	if err != nil {
		return nil, false, err
	}
	// packet validation BEFORE any dispatch (CP-2 F4): nonblank delta,
	// unique/nonblank IDs, submitted ⊆ current outstanding, and every ID a
	// dispositioned (closed) finding. Invalid packets never reach the reviewer.
	if strings.TrimSpace(claimedDelta) == "" {
		return nil, false, fmt.Errorf("confirmation requires a nonblank claimed delta: fail-closed (no dispatch)")
	}
	outSet := map[string]bool{}
	for _, id := range cs.Outstanding {
		outSet[id] = true
	}
	idSeen := map[string]bool{}
	byID := map[string]review.Finding{}
	for _, f := range st.Findings {
		byID[f.ID] = f
	}
	if len(ids) == 0 {
		return nil, false, fmt.Errorf("confirmation submission requires IDs: fail-closed (no dispatch)")
	}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || idSeen[id] {
			return nil, false, fmt.Errorf("confirmation IDs must be nonblank and unique: fail-closed (no dispatch)")
		}
		idSeen[id] = true
		if !outSet[id] {
			return nil, false, fmt.Errorf("confirmation ID %q is not outstanding in this cycle: fail-closed (no dispatch)", id)
		}
		if f, ok := byID[id]; !ok || f.Disposition == "" {
			return nil, false, fmt.Errorf("confirmation ID %q is not a dispositioned finding: fail-closed (no dispatch)", id)
		}
	}
	// precondition BEFORE any dispatch: exact target revision
	targetNow, _, terr := TargetSnapshot(st.TargetLocation)
	if terr != nil || expectedTargetRev != st.TargetRevision || targetNow != st.TargetRevision {
		cyc.RecordPreconditionFailure()
		cs.PreconditionFailures = cyc.PreconditionFailures
		return st, false, appendState(s.Canonical, st,
			fmt.Sprintf("\n## confirmation precondition-failure R%d\n- expected: %s\n- state: %s\n", roundIndex, expectedTargetRev, st.TargetRevision), loadRev)
	}
	// confirmation must run in the same reviewer session (contract): a
	// different vendor is only possible through the explicit reset path.
	if st.SessionRef != "" && st.Vendor == s.Adapter.Vendor() && req.ResumeRef == "" {
		req.ResumeRef = st.SessionRef
	} else if st.SessionRef != "" && st.Vendor != s.Adapter.Vendor() {
		return nil, false, fmt.Errorf("confirmation must use the stored reviewer session (%s): vendor switch is not allowed inside a confirmation cycle", st.Vendor)
	}
	req.SchemaJSON = ConfirmSchema
	req.Prompt = fmt.Sprintf(`Confirmation pass (bounded). You previously reviewed this target and requested changes.
Claimed delta: %s
For EACH of these finding IDs, judge only whether the claimed fix is actually reflected: %s
Output per the schema: results[] with id and status confirmed|not-confirmed. Do not issue a verdict, do not report new findings.`,
		claimedDelta, strings.Join(ids, ", "))

	if err := s.Adapter.PreDispatch(ctx, req, s.Handles); err != nil {
		return nil, false, fmt.Errorf("confirmation pre-dispatch failure (nothing consumed): %w", err)
	}
	snapshot, err := store.Revision(s.Canonical)
	if err != nil {
		return nil, false, err
	}
	res, dispatchErr := s.Adapter.Dispatch(ctx, req, s.Handles)
	if res != nil && !res.Started {
		return nil, false, fmt.Errorf("confirmation child start failure (nothing consumed): %w", dispatchErr)
	}
	label := fmt.Sprintf("conf-r%d-try%d", roundIndex, cyc.ValidAttempts+cyc.PreconditionFailures+1)
	section := fmt.Sprintf("\n## confirmation attempt R%d\n- submitted: %s\n- claimed_delta: %q\n", roundIndex, strings.Join(ids, ", "), claimedDelta)
	if res != nil {
		prov, _ := json.Marshal(res.Provenance)
		section += fmt.Sprintf("- provenance: %s\n", prov)
		section += store.EncodeBlock("raw_stdout "+label, res.Stdout)
		section += store.EncodeBlock("raw_stderr "+label, res.Stderr)
	}
	done := false
	if dispatchErr != nil || res == nil || res.Structured == nil {
		// failed or schema-less output: a validation failure, not a valid attempt
		cyc.RecordPreconditionFailure()
		section += fmt.Sprintf("- result: invalid (dispatch error or missing structured output)\n")
	} else if confirmed, perrs := parseConfirmResults(res.Structured, ids); len(perrs) > 0 {
		cyc.RecordPreconditionFailure()
		section += fmt.Sprintf("- result: invalid (%s) — no valid attempt consumed\n", strings.Join(perrs, "; "))
	} else {
		if err := cyc.SubmitValidAttempt(ids, confirmed); err != nil {
			return nil, false, err
		}
		done = cyc.Done()
		section += fmt.Sprintf("- confirmed: %s\n- outstanding: %s\n- escalated: %v\n",
			strings.Join(confirmed, ", "), strings.Join(cyc.Outstanding(), ", "), cyc.Escalated)
	}
	if res != nil && res.Provenance.SessionRef != "" {
		st.SessionRef, st.Vendor = res.Provenance.SessionRef, s.Adapter.Vendor()
	}
	cs.Outstanding = cyc.Outstanding()
	cs.ValidAttempts = cyc.ValidAttempts
	cs.PreconditionFailures = cyc.PreconditionFailures
	cs.Escalated = cyc.Escalated
	block, err := stateSection(st)
	if err != nil {
		return nil, false, err
	}
	if _, err := store.AppendAtomic(s.Canonical, section+block, snapshot); err != nil {
		rpath, rerr := writeRecovery(s.Canonical, section+block, st, snapshot)
		if rerr != nil {
			return nil, false, fmt.Errorf("confirmation append conflict AND recovery failed: %v / %v", err, rerr)
		}
		return nil, false, fmt.Errorf("confirmation append conflict: %v — preserved in %s; reconcile required", err, rpath)
	}
	return st, done, nil
}

// Disposition records the driver's response to a finding and persists it.
func Disposition(canonical, findingID string, d review.Disposition, decision *review.ArbiterDecision) (*State, error) {
	if !review.ValidDisposition(d) {
		return nil, fmt.Errorf("invalid disposition %q", d)
	}
	var out *State
	err := withCanonicalLock(canonical, func() error {
		rev, err := store.Revision(canonical)
		if err != nil {
			return err
		}
		st, err := LoadState(canonical)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("no objective in canonical")
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
			return fmt.Errorf("finding %s not found", findingID)
		}
		// Promotion to CLOSABLE happens here and only here (R1-CX-F1) — and
		// only when a valid, non-stale round reviewed the target bytes on disk
		// right now (Gate A-3: a post-result target edit blocks promotion).
		if st.Governance == string(kernel.GovDecisionRequired) && review.ClosureCheck(st.Findings) == nil &&
			closableAgainstDisk(st) == nil {
			st.Governance = string(kernel.GovClosable)
		}
		out = st
		return appendState(canonical, st, fmt.Sprintf("\n## disposition %s\n- decision: %s\n", findingID, d), rev)
	})
	return out, err
}

func hasValidRound(st *State) bool {
	for _, r := range st.Rounds {
		if r.Outcome == string(review.OutcomeResultValid) && !r.Stale {
			return true
		}
	}
	return false
}

// unknownRound returns the index of the first round whose latest attempt is
// UNKNOWN, or -1. An UNKNOWN round blocks any further dispatch until an owner
// resolves it (R0-F3).
func unknownRound(st *State) int {
	for _, r := range st.Rounds {
		if len(r.Attempts) > 0 && r.Attempts[len(r.Attempts)-1] == string(kernel.ExecUnknown) {
			return r.Index
		}
	}
	return -1
}

// latestReviewedCurrent reports whether the most recent round is a
// result-valid, non-stale review of exactly the current TargetRevision. It
// is the precondition for advancing (R0-F1): advancement continues the loop
// from a completed review of the present bytes, never from an older round, a
// failed/timeout/needs-input latest round, or an already-advanced revision
// that has not been re-reviewed.
func latestReviewedCurrent(st *State) bool {
	if len(st.Rounds) == 0 {
		return false
	}
	last := st.Rounds[len(st.Rounds)-1]
	return last.Outcome == string(review.OutcomeResultValid) && !last.Stale && last.Revision == st.TargetRevision
}

// closableAgainstDisk is the fail-closed closure precondition (Gate A-3):
// some valid, non-stale round must have reviewed exactly the target bytes
// that are on disk right now. A target edited after the reviewed round can
// never close as if the reviewed revision were current — the driver either
// advances the objective (and reviews again) or opens a follow-up.
func closableAgainstDisk(st *State) error {
	diskNow, _, err := TargetSnapshot(st.TargetLocation)
	if err != nil {
		return fmt.Errorf("closure target snapshot: %w", err)
	}
	if diskNow != st.TargetRevision {
		return fmt.Errorf("target %s changed after the reviewed round (stale): advance the objective or open a follow-up", st.TargetLocation)
	}
	for _, r := range st.Rounds {
		if r.Outcome == string(review.OutcomeResultValid) && !r.Stale && r.Revision == diskNow {
			return nil
		}
	}
	return fmt.Errorf("no valid non-stale round reviewed the current target revision: fail-closed")
}

// Close ends the objective through the fail-closed gate. It never promotes:
// only a persisted CLOSABLE state can close (R1-CX-F1).
func Close(canonical string) (*State, error) {
	var out *State
	err := withCanonicalLock(canonical, func() error {
		rev, err := store.Revision(canonical)
		if err != nil {
			return err
		}
		st, err := LoadState(canonical)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("no objective in canonical")
		}
		if st.Governance != string(kernel.GovClosable) {
			return fmt.Errorf("close refused: governance is %s, not CLOSABLE (a valid review result and complete dispositions are required)", st.Governance)
		}
		// Close-time re-verification (Gate A-3): a persisted CLOSABLE is not
		// enough — the target on disk must still be the reviewed revision.
		if err := closableAgainstDisk(st); err != nil {
			return fmt.Errorf("close refused: %w", err)
		}
		o, err := rehydrate(st)
		if err != nil {
			return err
		}
		if err := o.Close(func() error { return review.ClosureCheck(st.Findings) }); err != nil {
			return err
		}
		st.Governance = string(kernel.GovClosed)
		out = st
		return appendState(canonical, st, "\n## closure\n- result: CLOSED\n", rev)
	})
	return out, err
}

// Advance authorizes the objective's expected target revision to move to
// the bytes currently on disk — the explicit continuation of the
// review→revise→re-review loop inside one objective (Gate A-1). It is never
// silent: it refuses when the target is unchanged, when any blocking finding
// is undispositioned, or when the LATEST round is not a result-valid,
// non-stale review of the current revision (R0-F1) — so a double-advance
// (advance again before re-reviewing) or advancing off a failed/timeout
// latest round are both rejected. The advanced revision is un-reviewed, so
// the objective drops out of CLOSABLE and only a new round over the advanced
// revision can make it closable again. Advancing consumes no round.
func Advance(canonical, note string) (*State, error) {
	var out *State
	err := withCanonicalLock(canonical, func() error {
		rev, err := store.Revision(canonical)
		if err != nil {
			return err
		}
		st, err := LoadState(canonical)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("no objective in canonical")
		}
		switch st.Governance {
		case string(kernel.GovClosed), string(kernel.GovSuperseded), string(kernel.GovAbandoned):
			return fmt.Errorf("advance refused: objective is terminal (%s)", st.Governance)
		}
		if !latestReviewedCurrent(st) {
			return fmt.Errorf("advance refused: the latest round must be a result-valid, non-stale review of the current revision (advance continues the loop from a completed re-review, not an older or failed round)")
		}
		if err := review.ClosureCheck(st.Findings); err != nil {
			return fmt.Errorf("advance refused: blocking findings are not fully dispositioned: %w", err)
		}
		diskNow, resolved, err := TargetSnapshot(st.TargetLocation)
		if err != nil {
			return err
		}
		if diskNow == st.TargetRevision {
			return fmt.Errorf("advance refused: target unchanged — nothing to advance")
		}
		from := st.TargetRevision
		afterRound := st.Rounds[len(st.Rounds)-1].Index
		st.Advances = append(st.Advances, AdvanceState{
			FromRevision: from, ToRevision: diskNow, AfterRound: afterRound, Note: note,
		})
		st.TargetRevision, st.TargetResolved = diskNow, resolved
		if st.Governance == string(kernel.GovClosable) {
			st.Governance = string(kernel.GovDecisionRequired) // kernel-legal: CLOSABLE → DECISION_REQUIRED
		}
		out = st
		return appendState(canonical, st, fmt.Sprintf(
			"\n## advance after R%d\n- from: %s\n- to: %s\n- note: %s\n", afterRound, from, diskNow, note), rev)
	})
	return out, err
}

// Terminate ends the objective as SUPERSEDED or ABANDONED with arbiter
// identity and reason.
func Terminate(canonical string, to kernel.GovernanceState, arbiter, reason string) (*State, error) {
	var out *State
	err := withCanonicalLock(canonical, func() error {
		rev, err := store.Revision(canonical)
		if err != nil {
			return err
		}
		st, err := LoadState(canonical)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("no objective in canonical")
		}
		o, err := rehydrate(st)
		if err != nil {
			return err
		}
		if err := o.Terminate(to, kernel.TerminalReason{Arbiter: arbiter, Reason: reason}); err != nil {
			return err
		}
		st.Governance = string(to)
		st.TerminalArbiter, st.TerminalReason = arbiter, reason
		out = st
		return appendState(canonical, st, fmt.Sprintf("\n## terminal\n- result: %s\n- arbiter: %s\n- reason: %s\n", to, arbiter, reason), rev)
	})
	return out, err
}

// appendState appends the state block under a compare-and-swap on
// expectedRev — the revision observed at the start of the mutator's critical
// section (R0-F2). A concurrent writer that changed the canonical since then
// fails the CAS instead of being silently overwritten.
func appendState(canonical string, st *State, header, expectedRev string) error {
	block, err := stateSection(st)
	if err != nil {
		return err
	}
	_, err = store.AppendAtomic(canonical, header+block, expectedRev)
	return err
}

// withCanonicalLock serializes a mutator's whole load→validate→mutate→append
// critical section across processes via an exclusive flock on a sidecar lock
// file (R0-F2). Two acrelay processes can no longer interleave read-modify-
// write and drop each other's state blocks; the CAS in appendState remains as
// defense in depth against any non-locking writer.
func withCanonicalLock(canonical string, fn func() error) error {
	fd, err := os.OpenFile(canonical+".lock", os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer fd.Close()
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("canonical lock failed: fail-closed, refusing unserialized mutation: %w", err)
	}
	defer syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	return fn()
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
	fmt.Fprintf(&b, "objective %s (%s)\ngovernance: %s\nrounds: %d/%d\ntarget: %s sha256=%s\n",
		st.ObjectiveID, st.Question, st.Governance, len(st.Rounds), kernel.MaxRoundsPerObjective,
		st.TargetLocation, st.TargetRevision[:12])
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
	if recs, _ := pendingRecoveries(canonical); len(recs) > 0 {
		fmt.Fprintf(&b, "PENDING RECOVERY: %s (reconcile required before dispatch)\n", strings.Join(recs, ", "))
	}
	return b.String(), nil
}
