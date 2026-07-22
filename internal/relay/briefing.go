package relay

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/store"
	"github.com/kyungseo/acrelay/internal/subject"
)

// BriefingOutputVersion: v0.2 adds the allowlisted topology block (derived
// profile, driver-session-separation state, and source-qualified facets) —
// FEAT-20260722-001. The DTO extension is a version bump, not an in-place
// change (R0-CX-F4).
const BriefingOutputVersion = "briefing-output v0.2"

type BriefingReadiness string

const (
	BriefingReady             BriefingReadiness = "ready"
	BriefingReadyWithCautions BriefingReadiness = "ready-with-cautions"
	BriefingBlocked           BriefingReadiness = "blocked"
	BriefingTerminal          BriefingReadiness = "terminal"
)

const (
	BriefingCheckReady             = 0
	BriefingCheckReadyWithCautions = 3
	BriefingCheckBlocked           = 4
	BriefingCheckTerminal          = 5
)

// BriefingReason is a path-safe, stable machine classification. It contains
// only relay identities and logical references; filesystem and raw error
// strings never cross the briefing DTO boundary.
type BriefingReason struct {
	Code       string `json:"code"`
	RoundIndex *int   `json:"round_index,omitempty"`
	FindingID  string `json:"finding_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Count      int    `json:"count,omitempty"`
}

type BriefingAction struct {
	Code       string `json:"code"`
	FindingID  string `json:"finding_id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	RoundIndex *int   `json:"round_index,omitempty"`
}

type BriefingObjective struct {
	CollaborationID  string `json:"collaboration_id"`
	ObjectiveID      string `json:"objective_id"`
	Question         string `json:"question"`
	Governance       string `json:"governance"`
	FormalRoundBound int    `json:"formal_round_bound"`
	RoundsUsed       int    `json:"rounds_used"`
	Vendor           string `json:"vendor,omitempty"`
	TerminalArbiter  string `json:"terminal_arbiter,omitempty"`
	TerminalReason   string `json:"terminal_reason,omitempty"`
	CloseActor       string `json:"close_actor,omitempty"`
	CloseRole        string `json:"close_role,omitempty"`
	CloseAuthority   string `json:"close_authority,omitempty"`
}

// BriefingTopology is the allowlisted topology projection: closed-enum relay
// values only — no session ref, native handle, path, or raw input crosses it.
type BriefingTopology struct {
	Version                 string          `json:"version"`
	Profile                 string          `json:"profile"`
	DriverSessionSeparation string          `json:"driver_session_separation"`
	Facets                  []TopologyFacet `json:"facets"`
}

type BriefingTrustApproval struct {
	ID       string `json:"id"`
	Actor    string `json:"actor"`
	Decision string `json:"decision"`
	Scope    string `json:"scope"`
}

type BriefingTrust struct {
	Version        string                  `json:"version"`
	ProfileID      string                  `json:"profile_id"`
	WorkingDirMode string                  `json:"working_dir_mode"`
	Approvals      []BriefingTrustApproval `json:"approvals"`
}

type BriefingMember struct {
	Kind        string `json:"kind"`
	LogicalPath string `json:"logical_path"`
	Digest      string `json:"digest"`
	Bytes       int64  `json:"bytes"`
}

type BriefingSubject struct {
	Kind      string           `json:"kind"`
	Aggregate string           `json:"aggregate"`
	Members   []BriefingMember `json:"members"`
}

type BriefingRound struct {
	Index         int    `json:"index"`
	Execution     string `json:"execution,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	Verdict       string `json:"verdict,omitempty"`
	Revision      string `json:"revision,omitempty"`
	Stale         bool   `json:"stale"`
	Contradiction bool   `json:"contradiction"`
}

type BriefingEvidence struct {
	ID                string `json:"id"`
	Round             int    `json:"round"`
	Member            string `json:"member"`
	MemberDigest      string `json:"member_digest"`
	AggregateRevision string `json:"aggregate_revision"`
	LocationKind      string `json:"location_kind"`
	StartLine         int    `json:"start_line,omitempty"`
	EndLine           int    `json:"end_line,omitempty"`
	Claim             string `json:"claim"`
	Assurance         string `json:"assurance"`
	Normalization     string `json:"normalization,omitempty"`
}

type BriefingAssuranceCounts struct {
	ContentMatch           int `json:"content_match"`
	ContentMatchNormalized int `json:"content_match_normalized"`
	ReviewerDeclared       int `json:"reviewer_declared"`
}

type BriefingFinding struct {
	ID                string   `json:"id"`
	ReviewerSeverity  string   `json:"reviewer_severity"`
	Blocking          bool     `json:"blocking"`
	Summary           string   `json:"summary"`
	Evidence          []string `json:"evidence"`
	Recommendation    string   `json:"recommendation"`
	Disposition       string   `json:"disposition,omitempty"`
	Rationale         string   `json:"rationale,omitempty"`
	FollowUp          string   `json:"follow_up,omitempty"`
	ApprovalRequestID string   `json:"approval_request_id,omitempty"`
	Confirmation      string   `json:"confirmation"`
}

type BriefingApprovalOption struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type BriefingOwnerResponse struct {
	Actor                 string `json:"actor,omitempty"`
	RespondedAt           string `json:"responded_at,omitempty"`
	Decision              string `json:"decision,omitempty"`
	DecisionScope         string `json:"decision_scope,omitempty"`
	Unambiguous           bool   `json:"unambiguous"`
	VerbatimRecorded      bool   `json:"verbatim_recorded"`
	DurableAnchorRecorded bool   `json:"durable_anchor_recorded"`
}

type BriefingApprovalRequest struct {
	ID             string                   `json:"id"`
	Type           string                   `json:"type"`
	RequesterRole  string                   `json:"requester_role"`
	Requester      string                   `json:"requester"`
	Scope          string                   `json:"scope"`
	Reason         string                   `json:"reason"`
	Options        []BriefingApprovalOption `json:"options"`
	Status         string                   `json:"status"`
	TargetRevision string                   `json:"target_revision"`
	Stale          bool                     `json:"stale"`
	Response       *BriefingOwnerResponse   `json:"response,omitempty"`
}

type BriefingAdvance struct {
	FromRevision string `json:"from_revision"`
	ToRevision   string `json:"to_revision"`
	AfterRound   int    `json:"after_round"`
	Note         string `json:"note,omitempty"`
}

// Briefing is an allowlist projection. It intentionally has no session ref,
// native handle, raw reviewer block, subject root, resolved root/path,
// evidence excerpt, owner verbatim, or durable-anchor value.
type Briefing struct {
	Version            string                    `json:"version"`
	CanonicalRevision  string                    `json:"canonical_revision"`
	SubjectAggregate   string                    `json:"subject_aggregate"`
	Readiness          BriefingReadiness         `json:"readiness"`
	BlockingReasons    []BriefingReason          `json:"blocking_reasons"`
	Cautions           []BriefingReason          `json:"cautions"`
	NextActions        []BriefingAction          `json:"next_actions"`
	Objective          BriefingObjective         `json:"objective"`
	Topology           BriefingTopology          `json:"topology"`
	Trust              BriefingTrust             `json:"trust"`
	Subject            BriefingSubject           `json:"subject"`
	Rounds             []BriefingRound           `json:"rounds"`
	FinalReviewedRound *BriefingRound            `json:"final_reviewed_round,omitempty"`
	Evidence           []BriefingEvidence        `json:"evidence"`
	AssuranceCounts    BriefingAssuranceCounts   `json:"assurance_counts"`
	Findings           []BriefingFinding         `json:"findings"`
	ApprovalRequests   []BriefingApprovalRequest `json:"approval_requests"`
	Advances           []BriefingAdvance         `json:"advances"`
	ProjectionOnly     bool                      `json:"projection_only"`
	CloseRevalidation  bool                      `json:"close_revalidation_required"`
	Disclaimers        []string                  `json:"disclaimers"`
}

type closeReadinessEvaluation struct {
	Blockers   []BriefingReason
	pendingErr error
	targetErr  error
	closureErr error
}

func roundPointer(index int) *int { return &index }

func containsReason(reasons []BriefingReason, code string) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func latestExecution(st *State) (int, string) {
	if len(st.Rounds) == 0 {
		return -1, ""
	}
	round := st.Rounds[len(st.Rounds)-1]
	if len(round.Attempts) == 0 {
		return round.Index, ""
	}
	return round.Index, round.Attempts[len(round.Attempts)-1]
}

// evaluateCloseReadiness is the shared non-mutating Close gate. Briefing
// consumes only its typed reasons; Close reuses the same result under the
// canonical mutator lock and may return the private diagnostic cause.
func evaluateCloseReadiness(canonical string, st *State) closeReadinessEvaluation {
	var out closeReadinessEvaluation
	add := func(reason BriefingReason) {
		for _, existing := range out.Blockers {
			if existing.Code == reason.Code && existing.FindingID == reason.FindingID &&
				existing.RequestID == reason.RequestID {
				return
			}
		}
		out.Blockers = append(out.Blockers, reason)
	}

	if pending, err := pendingTransactions(canonical); err != nil {
		out.pendingErr = err
		add(BriefingReason{Code: "pending-state-unavailable"})
	} else if len(pending) > 0 {
		out.pendingErr = fmt.Errorf("pending transactions exist: reconcile or declared abandon required before mutation")
		add(BriefingReason{Code: "pending-transaction", Count: len(pending)})
	}

	if kernel.IsGovTerminal(kernel.GovernanceState(st.Governance)) {
		add(BriefingReason{Code: "terminal-state"})
		return out
	}
	if st.Governance != string(kernel.GovClosable) {
		add(BriefingReason{Code: "governance-not-closable"})
	}

	if round, execution := latestExecution(st); execution == string(kernel.ExecUnknown) {
		add(BriefingReason{Code: "execution-unknown", RoundIndex: roundPointer(round)})
	} else if execution == string(kernel.ExecFailed) {
		add(BriefingReason{Code: "execution-failed", RoundIndex: roundPointer(round)})
	}
	if len(st.Rounds) > 0 {
		last := st.Rounds[len(st.Rounds)-1]
		if last.Contradiction && st.Governance != string(kernel.GovClosable) {
			add(BriefingReason{Code: "verdict-contradiction", RoundIndex: roundPointer(last.Index)})
		}
	}

	diskNow, err := subject.Resolve(st.SubjectSpec)
	if err != nil {
		out.targetErr = err
		add(BriefingReason{Code: "subject-unavailable"})
	} else if diskNow.Aggregate != st.TargetRevision {
		out.targetErr = fmt.Errorf("subject changed after the reviewed round (stale): advance the objective or open a follow-up")
		add(BriefingReason{Code: "stale-target"})
	} else {
		validCurrent := false
		for _, round := range st.Rounds {
			if round.Outcome == string(review.OutcomeResultValid) && !round.Stale && round.Revision == diskNow.Aggregate {
				validCurrent = true
				break
			}
		}
		if !validCurrent {
			out.targetErr = fmt.Errorf("no valid non-stale round reviewed the current target revision: fail-closed")
			add(BriefingReason{Code: "no-current-review"})
		}
	}

	for _, finding := range st.Findings {
		if finding.Blocking && finding.Disposition == "" {
			add(BriefingReason{Code: "blocking-finding-undispositioned", FindingID: finding.ID})
		}
	}
	for _, request := range st.ApprovalRequests {
		if request.Status == review.ApprovalOpen {
			code := "approval-open"
			if request.Stale {
				code = "approval-stale"
			}
			add(BriefingReason{Code: code, RequestID: request.ID})
		}
	}
	if err := review.ClosureCheckForClose(st.Findings, st.ApprovalRequests); err != nil {
		out.closureErr = err
		if !containsReason(out.Blockers, "blocking-finding-undispositioned") &&
			!containsReason(out.Blockers, "approval-open") && !containsReason(out.Blockers, "approval-stale") {
			add(BriefingReason{Code: "closure-check-failed"})
		}
	}

	if st.FormalRoundBound > 0 && len(st.Rounds) >= st.FormalRoundBound &&
		(containsReason(out.Blockers, "no-current-review") || containsReason(out.Blockers, "execution-failed") ||
			containsReason(out.Blockers, "execution-unknown") || containsReason(out.Blockers, "verdict-contradiction")) {
		add(BriefingReason{Code: "round-bound-exhausted", Count: st.FormalRoundBound})
	}
	return out
}

func (r closeReadinessEvaluation) closeError(st *State) error {
	if r.pendingErr != nil {
		return r.pendingErr
	}
	if kernel.IsGovTerminal(kernel.GovernanceState(st.Governance)) || st.Governance != string(kernel.GovClosable) {
		return fmt.Errorf("governance is %s, not CLOSABLE (a valid review result and complete dispositions are required)", st.Governance)
	}
	if r.targetErr != nil {
		return r.targetErr
	}
	if r.closureErr != nil {
		return r.closureErr
	}
	return fmt.Errorf("close readiness blocked")
}

func confirmationStatus(id string, confirmations []ConfState) string {
	for _, confirmation := range confirmations {
		present := false
		for _, initial := range confirmation.Initial {
			if initial == id {
				present = true
				break
			}
		}
		if !present {
			continue
		}
		for _, outstanding := range confirmation.Outstanding {
			if outstanding != id {
				continue
			}
			if confirmation.Escalated {
				return "escalated"
			}
			if confirmation.ValidAttempts == 0 {
				return "pending"
			}
			return "not-confirmed"
		}
		return "confirmed"
	}
	return "not-requested"
}

func briefingCautions(st *State) []BriefingReason {
	var cautions []BriefingReason
	for _, confirmation := range st.Confirmations {
		for _, id := range confirmation.Outstanding {
			code := "confirmation-pending"
			if confirmation.Escalated {
				code = "confirmation-escalated"
			} else if confirmation.ValidAttempts > 0 {
				code = "confirmation-not-confirmed"
			}
			cautions = append(cautions, BriefingReason{
				Code: code, RoundIndex: roundPointer(confirmation.RoundIndex), FindingID: id,
			})
		}
	}
	for _, round := range st.Rounds {
		if round.Contradiction && st.Governance == string(kernel.GovClosable) {
			cautions = append(cautions, BriefingReason{
				Code: "verdict-contradiction-history", RoundIndex: roundPointer(round.Index),
			})
		}
	}
	// Topology cautions (FEAT-20260722-001): same-vendor correlation,
	// undeclared relations, and non-fresh reviewer sessions are surfaced in
	// the result, never silently omitted (R1-CX-N1: relation facts, not an
	// ordinal independence ranking).
	switch DerivedTopologyProfile(st) {
	case ProfileSameVendorExt:
		cautions = append(cautions, BriefingReason{Code: "same-vendor-review"})
	case ProfileUndeclared:
		cautions = append(cautions, BriefingReason{Code: "topology-undeclared"})
	}
	// A resumed or carried reviewer session is not a fresh review context
	// (R0-CX-F2) — related objectives carry the prior session by contract.
	if st.SessionRef != "" && st.ReviewerSessionMode != SessionModeNew && st.ReviewerSessionMode != SessionModeReset {
		cautions = append(cautions, BriefingReason{Code: "reviewer-session-resumed"})
	}
	return cautions
}

func briefingActions(readiness BriefingReadiness, blockers, cautions []BriefingReason) []BriefingAction {
	if readiness == BriefingTerminal {
		return []BriefingAction{{Code: "none-terminal"}}
	}
	var actions []BriefingAction
	seen := map[string]bool{}
	add := func(action BriefingAction) {
		key := action.Code + "|" + action.FindingID + "|" + action.RequestID
		if !seen[key] {
			seen[key] = true
			actions = append(actions, action)
		}
	}
	for _, reason := range blockers {
		action := BriefingAction{RoundIndex: reason.RoundIndex, FindingID: reason.FindingID, RequestID: reason.RequestID}
		switch reason.Code {
		case "pending-transaction", "pending-state-unavailable":
			action.Code = "reconcile-or-abandon-transaction"
		case "blocking-finding-undispositioned", "verdict-contradiction":
			action.Code = "resolve-finding"
		case "approval-open", "approval-stale":
			action.Code = "resolve-approval"
		case "round-bound-exhausted", "execution-unknown":
			action.Code = "open-follow-up-objective"
		case "stale-target", "no-current-review", "execution-failed", "governance-not-closable":
			action.Code = "review-or-follow-up"
		case "subject-unavailable":
			action.Code = "inspect-subject"
		default:
			action.Code = "inspect-blocker"
		}
		add(action)
	}
	if len(blockers) == 0 {
		if len(cautions) == 0 {
			add(BriefingAction{Code: "owner-may-accept-or-close"})
		} else {
			add(BriefingAction{Code: "owner-review-cautions"})
			add(BriefingAction{Code: "owner-may-accept-or-close"})
		}
	}
	return actions
}

func briefingRound(round RoundState) BriefingRound {
	execution := ""
	if len(round.Attempts) > 0 {
		execution = round.Attempts[len(round.Attempts)-1]
	}
	return BriefingRound{
		Index: round.Index, Execution: execution, Outcome: round.Outcome, Verdict: round.Verdict,
		Revision: round.Revision, Stale: round.Stale, Contradiction: round.Contradiction,
	}
}

// BuildBriefing produces the local-private, read-only projection from one
// canonical snapshot. It performs no append, lock creation, round admission,
// dispatch, retry, acknowledgment, or Close transition.
func BuildBriefing(canonical string) (*Briefing, error) {
	doc, err := store.ReadAll(canonical)
	if err != nil {
		return nil, err
	}
	st, err := loadStateDocument(doc)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("no objective in canonical")
	}

	evaluation := evaluateCloseReadiness(canonical, st)
	cautions := briefingCautions(st)
	blockers := evaluation.Blockers
	readiness := BriefingReady
	if kernel.IsGovTerminal(kernel.GovernanceState(st.Governance)) {
		readiness = BriefingTerminal
		blockers = nil
	} else if len(evaluation.Blockers) > 0 {
		readiness = BriefingBlocked
	} else if len(cautions) > 0 {
		readiness = BriefingReadyWithCautions
	}

	b := &Briefing{
		Version: BriefingOutputVersion, CanonicalRevision: store.Digest([]byte(doc)),
		SubjectAggregate: st.TargetRevision, Readiness: readiness,
		BlockingReasons: blockers, Cautions: cautions,
		Objective: BriefingObjective{
			CollaborationID: st.CollaborationID, ObjectiveID: st.ObjectiveID, Question: st.Question,
			Governance: st.Governance, FormalRoundBound: st.FormalRoundBound, RoundsUsed: len(st.Rounds),
			Vendor: st.Vendor, TerminalArbiter: st.TerminalArbiter, TerminalReason: st.TerminalReason,
			CloseActor: st.CloseActor, CloseRole: st.CloseRole, CloseAuthority: st.CloseAuthority,
		},
		Topology: BriefingTopology{
			Version: TopologyVersion, Profile: DerivedTopologyProfile(st),
			DriverSessionSeparation: DriverSessionSeparation(st),
			Facets:                  TopologyFacets(st),
		},
		Trust: BriefingTrust{
			Version: st.TrustPolicy.Version, ProfileID: st.TrustPolicy.ProfileID,
			WorkingDirMode: st.TrustPolicy.WorkingDirMode,
		},
		Subject:        BriefingSubject{Kind: string(st.SubjectSpec.Kind), Aggregate: st.Subject.Aggregate},
		ProjectionOnly: true, CloseRevalidation: true,
		Disclaimers: []string{
			"This briefing is a read-only projection and does not approve, close, dispatch, or retry.",
			"The actual close command revalidates canonical, target, and pending transaction state.",
			"content-match proves only that the reviewer returned matching bytes; it does not prove understanding or completeness.",
			"Topology facets are operator-declared or runtime-observed provenance; reviewer independence is never verified. Same-vendor and shared-context review keeps correlated blind spots a separate session cannot remove.",
		},
	}

	for _, approval := range st.TrustPolicy.Approvals {
		b.Trust.Approvals = append(b.Trust.Approvals, BriefingTrustApproval{
			ID: approval.ID, Actor: approval.Actor, Decision: approval.Decision, Scope: approval.Scope,
		})
	}
	for _, member := range st.Subject.Members {
		b.Subject.Members = append(b.Subject.Members, BriefingMember{
			Kind: member.Kind, LogicalPath: member.LogicalPath, Digest: member.Digest, Bytes: member.Bytes,
		})
	}
	for _, round := range st.Rounds {
		projected := briefingRound(round)
		b.Rounds = append(b.Rounds, projected)
		if round.Outcome == string(review.OutcomeResultValid) && !round.Stale && round.Revision == st.TargetRevision {
			copy := projected
			b.FinalReviewedRound = &copy
		}
	}
	for _, anchor := range st.Evidence {
		b.Evidence = append(b.Evidence, BriefingEvidence{
			ID: anchor.ID, Round: anchor.Round, Member: anchor.Member, MemberDigest: anchor.MemberDigest,
			AggregateRevision: anchor.AggregateRevision, LocationKind: anchor.LocationKind,
			StartLine: anchor.StartLine, EndLine: anchor.EndLine, Claim: anchor.Claim,
			Assurance: anchor.Assurance, Normalization: anchor.Normalization,
		})
		switch anchor.Assurance {
		case review.AssuranceContentMatch:
			b.AssuranceCounts.ContentMatch++
		case review.AssuranceContentMatchNormalized:
			b.AssuranceCounts.ContentMatchNormalized++
		case review.AssuranceReviewerDeclared:
			b.AssuranceCounts.ReviewerDeclared++
		}
	}
	for _, finding := range st.Findings {
		b.Findings = append(b.Findings, BriefingFinding{
			ID: finding.ID, ReviewerSeverity: finding.ReviewerSeverity, Blocking: finding.Blocking,
			Summary: finding.Summary, Evidence: append([]string(nil), finding.Evidence...),
			Recommendation: finding.Recommendation, Disposition: string(finding.Disposition),
			Rationale: finding.Rationale, FollowUp: finding.FollowUp,
			ApprovalRequestID: finding.ApprovalRequestID,
			Confirmation:      confirmationStatus(finding.ID, st.Confirmations),
		})
	}
	for _, request := range st.ApprovalRequests {
		projected := BriefingApprovalRequest{
			ID: request.ID, Type: request.Type, RequesterRole: request.RequesterRole,
			Requester: request.Requester, Scope: request.Scope, Reason: request.Reason,
			Status: request.Status, TargetRevision: request.TargetRevision, Stale: request.Stale,
		}
		for _, option := range request.Options {
			projected.Options = append(projected.Options, BriefingApprovalOption{
				ID: option.ID, Description: option.Description,
			})
		}
		if request.Response != nil {
			projected.Response = &BriefingOwnerResponse{
				Actor: request.Response.Actor, RespondedAt: request.Response.RespondedAt,
				Decision: request.Response.Decision, DecisionScope: request.Response.DecisionScope,
				Unambiguous:           request.Response.Unambiguous,
				VerbatimRecorded:      strings.TrimSpace(request.Response.Verbatim) != "",
				DurableAnchorRecorded: strings.TrimSpace(request.Response.DurableAnchor) != "",
			}
		}
		b.ApprovalRequests = append(b.ApprovalRequests, projected)
	}
	for _, advance := range st.Advances {
		b.Advances = append(b.Advances, BriefingAdvance{
			FromRevision: advance.FromRevision, ToRevision: advance.ToRevision,
			AfterRound: advance.AfterRound, Note: advance.Note,
		})
	}
	b.NextActions = briefingActions(readiness, b.BlockingReasons, b.Cautions)
	return b, nil
}

func BriefingCheckExitCode(readiness BriefingReadiness) int {
	switch readiness {
	case BriefingReady:
		return BriefingCheckReady
	case BriefingReadyWithCautions:
		return BriefingCheckReadyWithCautions
	case BriefingBlocked:
		return BriefingCheckBlocked
	case BriefingTerminal:
		return BriefingCheckTerminal
	default:
		return BriefingCheckBlocked
	}
}

func reasonContext(reason BriefingReason) string {
	var parts []string
	if reason.RoundIndex != nil {
		parts = append(parts, fmt.Sprintf("round=R%d", *reason.RoundIndex))
	}
	if reason.FindingID != "" {
		parts = append(parts, "finding="+reason.FindingID)
	}
	if reason.RequestID != "" {
		parts = append(parts, "request="+reason.RequestID)
	}
	if reason.Count > 0 {
		parts = append(parts, fmt.Sprintf("count=%d", reason.Count))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, " ") + ")"
}

var reasonMessages = map[string]string{
	"pending-state-unavailable":        "pending transaction state could not be inspected",
	"pending-transaction":              "a dispatch transaction is pending reconciliation or declared abandon",
	"terminal-state":                   "the objective is terminal",
	"governance-not-closable":          "governance has not reached CLOSABLE",
	"execution-unknown":                "the latest execution is UNKNOWN and cannot be retried automatically",
	"execution-failed":                 "the latest execution FAILED",
	"verdict-contradiction":            "approve conflicted with runtime-blocking evidence",
	"subject-unavailable":              "the subject could not be revalidated",
	"stale-target":                     "the subject no longer matches the reviewed target revision",
	"no-current-review":                "no valid non-stale review covers the current target revision",
	"blocking-finding-undispositioned": "a blocking finding has no driver disposition",
	"approval-open":                    "an owner approval request remains open",
	"approval-stale":                   "an owner approval request is open and stale",
	"closure-check-failed":             "the canonical closure check failed",
	"round-bound-exhausted":            "the formal round bound is exhausted",
	"confirmation-pending":             "a confirmation judgment is pending",
	"confirmation-not-confirmed":       "a dispositioned finding was not confirmed",
	"confirmation-escalated":           "confirmation attempts were exhausted and require owner judgment",
	"verdict-contradiction-history":    "the review history contains an approve/blocking contradiction",
	"same-vendor-review":               "driver and reviewer vendors are declared identical; correlated blind spots remain despite the separate reviewer session",
	"topology-undeclared":              "reviewer topology facets are undeclared; no independence relation can be derived",
	"reviewer-session-resumed":         "the reviewer session is resumed or carried from earlier work; this is not a fresh review context",
}

var actionMessages = map[string]string{
	"none-terminal":                    "No action; the objective is terminal.",
	"reconcile-or-abandon-transaction": "Reconcile or explicitly abandon the pending transaction.",
	"resolve-finding":                  "Disposition the finding or obtain the required owner decision.",
	"resolve-approval":                 "Record an exact owner response or withdraw the request.",
	"open-follow-up-objective":         "Open a follow-up objective; do not retry the consumed or unknown round.",
	"review-or-follow-up":              "Run an allowed review/advance path or open a follow-up objective.",
	"inspect-subject":                  "Restore a readable valid subject before continuing.",
	"inspect-blocker":                  "Inspect the typed blocker before continuing.",
	"owner-may-accept-or-close":        "Owner may accept and invoke close, which will revalidate all gates.",
	"owner-review-cautions":            "Owner must review every caution before accepting or closing.",
}

// RenderBriefingHuman renders only the allowlisted DTO. It never formats a
// State or private diagnostic error directly.
func RenderBriefingHuman(b *Briefing) string {
	var out strings.Builder
	fmt.Fprintf(&out, "ACRELAY CLOSEOUT BRIEFING (%s)\n", b.Version)
	fmt.Fprintf(&out, "objective: %s collaboration=%s question=%q\n",
		b.Objective.ObjectiveID, b.Objective.CollaborationID, b.Objective.Question)
	fmt.Fprintf(&out, "governance: %s\nreadiness: %s\n", b.Objective.Governance, b.Readiness)
	fmt.Fprintf(&out, "snapshot: canonical=%s subject=%s\n", b.CanonicalRevision, b.SubjectAggregate)
	fmt.Fprintf(&out, "rounds: %d/%d\n", b.Objective.RoundsUsed, b.Objective.FormalRoundBound)
	fmt.Fprintf(&out, "reviewer: vendor=%s trust-profile=%s mode=%s\n",
		b.Objective.Vendor, b.Trust.ProfileID, b.Trust.WorkingDirMode)
	fmt.Fprintf(&out, "topology: profile=%s driver_session_separation=%s (%s)\n",
		b.Topology.Profile, b.Topology.DriverSessionSeparation, b.Topology.Version)
	for _, fact := range b.Topology.Facets {
		fmt.Fprintf(&out, "- topology %s=%s (%s)\n", fact.Name, fact.Value, fact.Source)
	}
	for _, approval := range b.Trust.Approvals {
		fmt.Fprintf(&out, "- trust approval %s actor=%s decision=%s scope=%q\n",
			approval.ID, approval.Actor, approval.Decision, approval.Scope)
	}
	fmt.Fprintf(&out, "subject: kind=%s aggregate=%s members=%d\n",
		b.Subject.Kind, b.Subject.Aggregate, len(b.Subject.Members))
	for _, member := range b.Subject.Members {
		fmt.Fprintf(&out, "- member=%s kind=%s digest=%s bytes=%d\n",
			member.LogicalPath, member.Kind, member.Digest, member.Bytes)
	}
	if b.Readiness == BriefingTerminal {
		fmt.Fprintf(&out, "terminal: arbiter=%s reason=%q close-actor=%s close-role=%s close-authority=%q\n",
			b.Objective.TerminalArbiter, b.Objective.TerminalReason, b.Objective.CloseActor,
			b.Objective.CloseRole, b.Objective.CloseAuthority)
	}
	if b.FinalReviewedRound != nil {
		fmt.Fprintf(&out, "final reviewed round: R%d outcome=%s verdict=%s revision=%s\n",
			b.FinalReviewedRound.Index, b.FinalReviewedRound.Outcome,
			b.FinalReviewedRound.Verdict, b.FinalReviewedRound.Revision)
	} else {
		out.WriteString("final reviewed round: none for current target revision\n")
	}
	out.WriteString("round history:\n")
	if len(b.Rounds) == 0 {
		out.WriteString("- none\n")
	}
	for _, round := range b.Rounds {
		fmt.Fprintf(&out, "- R%d execution=%s outcome=%s verdict=%s revision=%s stale=%v contradiction=%v\n",
			round.Index, round.Execution, round.Outcome, round.Verdict, round.Revision,
			round.Stale, round.Contradiction)
	}

	out.WriteString("blocking reasons:\n")
	if len(b.BlockingReasons) == 0 {
		out.WriteString("- none\n")
	}
	for _, reason := range b.BlockingReasons {
		message := reasonMessages[reason.Code]
		if message == "" {
			message = "unrecognized typed blocker"
		}
		fmt.Fprintf(&out, "- %s: %s%s\n", reason.Code, message, reasonContext(reason))
	}
	out.WriteString("cautions:\n")
	if len(b.Cautions) == 0 {
		out.WriteString("- none\n")
	}
	for _, caution := range b.Cautions {
		message := reasonMessages[caution.Code]
		if message == "" {
			message = "unrecognized typed caution"
		}
		fmt.Fprintf(&out, "- %s: %s%s\n", caution.Code, message, reasonContext(caution))
	}

	fmt.Fprintf(&out, "evidence anchors: %d (content-match=%d content-match-normalized=%d reviewer-declared=%d)\n",
		len(b.Evidence), b.AssuranceCounts.ContentMatch, b.AssuranceCounts.ContentMatchNormalized,
		b.AssuranceCounts.ReviewerDeclared)
	for _, evidence := range b.Evidence {
		location := evidence.LocationKind
		if evidence.StartLine > 0 {
			location += fmt.Sprintf(":%d-%d", evidence.StartLine, evidence.EndLine)
		}
		fmt.Fprintf(&out, "- %s member=%s member-digest=%s aggregate=%s location=%s assurance=%s claim=%q\n",
			evidence.ID, evidence.Member, evidence.MemberDigest, evidence.AggregateRevision,
			location, evidence.Assurance, evidence.Claim)
	}

	out.WriteString("findings:\n")
	if len(b.Findings) == 0 {
		out.WriteString("- none\n")
	}
	for _, finding := range b.Findings {
		fmt.Fprintf(&out, "- %s severity=%s blocking=%v confirmation=%s summary=%q evidence=%s\n",
			finding.ID, finding.ReviewerSeverity, finding.Blocking, finding.Confirmation,
			finding.Summary, strings.Join(finding.Evidence, ","))
		fmt.Fprintf(&out, "  disposition=%s rationale=%q follow-up=%q recommendation=%q approval-request=%s\n",
			finding.Disposition, finding.Rationale, finding.FollowUp, finding.Recommendation,
			finding.ApprovalRequestID)
	}

	out.WriteString("approval requests:\n")
	if len(b.ApprovalRequests) == 0 {
		out.WriteString("- none\n")
	}
	for _, request := range b.ApprovalRequests {
		fmt.Fprintf(&out, "- %s type=%s status=%s stale=%v requester=%s/%s target=%s scope=%q reason=%q\n",
			request.ID, request.Type, request.Status, request.Stale,
			request.RequesterRole, request.Requester, request.TargetRevision, request.Scope, request.Reason)
		for _, option := range request.Options {
			fmt.Fprintf(&out, "  option %s: %s\n", option.ID, option.Description)
		}
		if request.Response != nil {
			fmt.Fprintf(&out, "  response actor=%s date=%s decision=%s scope=%q unambiguous=%v verbatim-recorded=%v durable-anchor-recorded=%v\n",
				request.Response.Actor, request.Response.RespondedAt, request.Response.Decision,
				request.Response.DecisionScope, request.Response.Unambiguous,
				request.Response.VerbatimRecorded, request.Response.DurableAnchorRecorded)
		}
	}

	out.WriteString("material delta:\n")
	material := false
	for _, finding := range b.Findings {
		if finding.FollowUp != "" {
			material = true
			fmt.Fprintf(&out, "- finding %s follow-up=%q\n", finding.ID, finding.FollowUp)
		}
	}
	for _, advance := range b.Advances {
		material = true
		fmt.Fprintf(&out, "- advance after R%d from=%s to=%s note=%q\n",
			advance.AfterRound, advance.FromRevision, advance.ToRevision, advance.Note)
	}
	if !material {
		out.WriteString("- none recorded in typed state\n")
	}

	out.WriteString("next actions:\n")
	for _, action := range b.NextActions {
		message := actionMessages[action.Code]
		if message == "" {
			message = "Inspect the typed action."
		}
		fmt.Fprintf(&out, "- %s: %s\n", action.Code, message)
	}
	for _, disclaimer := range b.Disclaimers {
		fmt.Fprintf(&out, "notice: %s\n", disclaimer)
	}
	return out.String()
}

func MarshalBriefingJSON(b *Briefing) ([]byte, error) {
	return json.MarshalIndent(b, "", "  ")
}
