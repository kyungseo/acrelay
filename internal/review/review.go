// Package review implements the versioned review profile: structured
// examined evidence, findings, approval requests, dispositions, and the
// fail-closed closure check (DR-811 §5-§6).
package review

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kyungseo/acrelay/internal/kernel"
)

type Verdict string

const (
	VerdictApprove          Verdict = "approve"
	VerdictChangesRequested Verdict = "changes-requested"
)

type Outcome string

const (
	OutcomeResultValid Outcome = "result-valid"
	OutcomeNeedsInput  Outcome = "needs-input"
	OutcomeFailed      Outcome = "failed"
)

type Disposition string

const (
	DispositionAccept    Disposition = "accept"
	DispositionRevise    Disposition = "revise"
	DispositionDefend    Disposition = "defend"
	DispositionNeedsUser Disposition = "needs-user"
)

func ValidDisposition(d Disposition) bool {
	switch d {
	case DispositionAccept, DispositionRevise, DispositionDefend, DispositionNeedsUser:
		return true
	}
	return false
}

const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"

	AssuranceContentMatch           = "content-match"
	AssuranceContentMatchNormalized = "content-match-normalized"
	AssuranceReviewerDeclared       = "reviewer-declared"

	NormalizationCRLFToLF = "crlf-to-lf"

	ApprovalOpen      = "open"
	ApprovalResolved  = "resolved"
	ApprovalWithdrawn = "withdrawn"

	MaxEvidenceLines = 40
	MaxEvidenceBytes = 4096
)

func ValidSeverity(s string) bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow:
		return true
	}
	return false
}

// BlockingForSeverity is review-profile v0.2's local policy. Reviewer output
// supplies severity evidence; the runtime, not the reviewer, owns Blocking.
func BlockingForSeverity(s string) bool { return s == SeverityCritical || s == SeverityHigh }

type LocationInput struct {
	Kind  string `json:"kind"`
	Start int    `json:"start,omitempty"`
	End   int    `json:"end,omitempty"`
}

type EvidenceInput struct {
	ID       string        `json:"id"`
	Member   string        `json:"member"`
	Location LocationInput `json:"location"`
	Excerpt  string        `json:"excerpt,omitempty"`
	Claim    string        `json:"claim"`
}

type FindingInput struct {
	Summary          string   `json:"summary"`
	ReviewerSeverity string   `json:"reviewer_severity"`
	Evidence         []string `json:"evidence"`
	Recommendation   string   `json:"recommendation"`
}

type ApprovalOption struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

type ApprovalRequestInput struct {
	Type    string           `json:"type"`
	Scope   string           `json:"scope"`
	Reason  string           `json:"reason"`
	Options []ApprovalOption `json:"options"`
}

// DispositionInput preserves the driver's complete canonical response. A
// rationale is mandatory for every disposition. Accept/revise additionally
// require an explicit follow-up ("no-action" is a valid explicit choice),
// while needs-user points to a separately durable approval request.
type DispositionInput struct {
	Decision          Disposition `json:"decision"`
	Rationale         string      `json:"rationale"`
	FollowUp          string      `json:"follow_up,omitempty"`
	ApprovalRequestID string      `json:"approval_request_id,omitempty"`
}

type ReviewResult struct {
	Verdict          Verdict                `json:"verdict"`
	Examined         []EvidenceInput        `json:"examined"`
	Findings         []FindingInput         `json:"findings"`
	ApprovalRequests []ApprovalRequestInput `json:"approval_requests"`
}

// EvidenceAnchor is normalized canonical evidence. A content-match proves
// only that the returned excerpt equals current member bytes; it does not
// prove model understanding or review completeness.
type EvidenceAnchor struct {
	ID                string `json:"id"`
	Round             int    `json:"round"`
	Member            string `json:"member"`
	MemberDigest      string `json:"member_digest"`
	AggregateRevision string `json:"aggregate_revision"`
	LocationKind      string `json:"location_kind"`
	StartLine         int    `json:"start_line,omitempty"`
	EndLine           int    `json:"end_line,omitempty"`
	Excerpt           string `json:"excerpt,omitempty"`
	Claim             string `json:"claim"`
	Assurance         string `json:"assurance"`
	Normalization     string `json:"normalization,omitempty"`
}

type Finding struct {
	ID                string      `json:"id"`
	ReviewerSeverity  string      `json:"reviewer_severity"`
	Blocking          bool        `json:"blocking"`
	Summary           string      `json:"summary"`
	Evidence          []string    `json:"evidence"`
	Recommendation    string      `json:"recommendation"`
	Disposition       Disposition `json:"disposition,omitempty"`
	Rationale         string      `json:"rationale,omitempty"`
	FollowUp          string      `json:"follow_up,omitempty"`
	ApprovalRequestID string      `json:"approval_request_id,omitempty"`
}

type OwnerResponse struct {
	Actor          string `json:"actor"`
	Verbatim       string `json:"verbatim"`
	RespondedAt    string `json:"responded_at"`
	Decision       string `json:"decision"`
	DecisionScope  string `json:"decision_scope"`
	DurableAnchor  string `json:"durable_anchor"`
	Unambiguous    bool   `json:"unambiguous"`
	ResolutionNote string `json:"resolution_note,omitempty"`
}

// ApprovalRequest is mutable review-time declared metadata. It is separate
// from immutable adapter TrustPolicy approvals and is not authentication or
// RBAC.
type ApprovalRequest struct {
	ID             string           `json:"id"`
	Type           string           `json:"type"`
	RequesterRole  string           `json:"requester_role"`
	Requester      string           `json:"requester"`
	Scope          string           `json:"scope"`
	Reason         string           `json:"reason"`
	Options        []ApprovalOption `json:"options"`
	Status         string           `json:"status"`
	TargetRevision string           `json:"target_revision"`
	Stale          bool             `json:"stale,omitempty"`
	Response       *OwnerResponse   `json:"response,omitempty"`
}

var namespacedType = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`)
var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidResponseDate(value string) bool {
	if _, err := time.Parse("2006-01-02", value); err == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func mapKeys(m map[string]any, allowed map[string]bool, prefix string, errs *[]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			*errs = append(*errs, prefix+"unknown-property:"+k)
		}
	}
}

func nonblank(v any) bool { s, ok := v.(string); return ok && strings.TrimSpace(s) != "" }

func integer(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// ValidateResult validates profile v0.2 structure. Content matching is a
// relay responsibility because it requires the authoritative subject bytes.
func ValidateResult(m map[string]any) []string {
	var errs []string
	mapKeys(m, map[string]bool{"verdict": true, "examined": true, "findings": true, "approval_requests": true}, "", &errs)
	verdict, vok := m["verdict"].(string)
	if _, ok := m["verdict"]; !ok {
		errs = append(errs, "missing:verdict")
	} else if !vok || (verdict != string(VerdictApprove) && verdict != string(VerdictChangesRequested)) {
		errs = append(errs, fmt.Sprintf("verdict-enum:%v", m["verdict"]))
	}

	examined, ok := m["examined"].([]any)
	if !ok {
		errs = append(errs, "examined-not-array")
	} else if len(examined) == 0 {
		errs = append(errs, "examined-empty")
	}
	anchorIDs := map[string]bool{}
	for i, raw := range examined {
		e, ok := raw.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("examined-%d-not-object", i))
			continue
		}
		p := fmt.Sprintf("examined-%d-", i)
		mapKeys(e, map[string]bool{"id": true, "member": true, "location": true, "excerpt": true, "claim": true}, p, &errs)
		id, _ := e["id"].(string)
		if strings.TrimSpace(id) == "" {
			errs = append(errs, p+"id-empty")
		} else if anchorIDs[id] {
			errs = append(errs, "duplicate-examined-id:"+id)
		}
		anchorIDs[id] = true
		if !nonblank(e["member"]) || !nonblank(e["claim"]) {
			errs = append(errs, p+"member-or-claim-empty")
		}
		loc, ok := e["location"].(map[string]any)
		if !ok {
			errs = append(errs, p+"location-not-object")
			continue
		}
		mapKeys(loc, map[string]bool{"kind": true, "start": true, "end": true}, p+"location-", &errs)
		kind, _ := loc["kind"].(string)
		switch kind {
		case "text-lines":
			start, sok := integer(loc["start"])
			end, eok := integer(loc["end"])
			if !sok || !eok || start < 1 || end < start {
				errs = append(errs, p+"line-range-invalid")
			}
			if !nonblank(e["excerpt"]) {
				errs = append(errs, p+"excerpt-empty")
			}
		case "opaque", "empty-member":
			if s, _ := e["excerpt"].(string); s != "" {
				errs = append(errs, p+kind+"-excerpt-forbidden")
			}
			if _, exists := loc["start"]; exists {
				errs = append(errs, p+kind+"-range-forbidden")
			}
			if _, exists := loc["end"]; exists {
				errs = append(errs, p+kind+"-range-forbidden")
			}
		default:
			errs = append(errs, p+"location-kind:"+kind)
		}
	}

	findings, ok := m["findings"].([]any)
	if !ok {
		errs = append(errs, "findings-not-array")
	}
	seenSummary := map[string]bool{}
	for i, raw := range findings {
		f, ok := raw.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("finding-%d-not-object", i))
			continue
		}
		p := fmt.Sprintf("finding-%d-", i)
		mapKeys(f, map[string]bool{"summary": true, "reviewer_severity": true, "evidence": true, "recommendation": true}, p, &errs)
		summary, _ := f["summary"].(string)
		if strings.TrimSpace(summary) == "" || !nonblank(f["recommendation"]) {
			errs = append(errs, p+"summary-or-recommendation-empty")
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(summary), " "))
		if normalized != "" && seenSummary[normalized] {
			errs = append(errs, "duplicate-finding:"+normalized)
		}
		seenSummary[normalized] = true
		severity, _ := f["reviewer_severity"].(string)
		if !ValidSeverity(severity) {
			errs = append(errs, p+"severity-enum:"+severity)
		}
		refs, ok := f["evidence"].([]any)
		if !ok || len(refs) == 0 {
			errs = append(errs, p+"evidence-empty")
			continue
		}
		seenRef := map[string]bool{}
		for _, rawRef := range refs {
			ref, ok := rawRef.(string)
			if !ok || !anchorIDs[ref] {
				errs = append(errs, p+"unknown-evidence-ref:"+fmt.Sprint(rawRef))
			} else if seenRef[ref] {
				errs = append(errs, p+"duplicate-evidence-ref:"+ref)
			}
			seenRef[ref] = true
		}
	}
	if verdict == string(VerdictChangesRequested) && len(findings) == 0 {
		errs = append(errs, "changes-requested-needs-finding")
	}

	requests, ok := m["approval_requests"].([]any)
	if !ok {
		errs = append(errs, "approval-requests-not-array")
	}
	seenRequest := map[string]bool{}
	for i, raw := range requests {
		r, ok := raw.(map[string]any)
		if !ok {
			errs = append(errs, fmt.Sprintf("approval-request-%d-not-object", i))
			continue
		}
		p := fmt.Sprintf("approval-request-%d-", i)
		mapKeys(r, map[string]bool{"type": true, "scope": true, "reason": true, "options": true}, p, &errs)
		typeName, _ := r["type"].(string)
		if !namespacedType.MatchString(typeName) || !nonblank(r["scope"]) || !nonblank(r["reason"]) {
			errs = append(errs, p+"type-scope-or-reason-invalid")
		}
		key := typeName + "\x00" + fmt.Sprint(r["scope"])
		if seenRequest[key] {
			errs = append(errs, p+"duplicate")
		}
		seenRequest[key] = true
		options, ok := r["options"].([]any)
		if !ok || len(options) == 0 {
			errs = append(errs, p+"options-empty")
			continue
		}
		seenOption := map[string]bool{}
		for j, rawOption := range options {
			o, ok := rawOption.(map[string]any)
			if !ok {
				errs = append(errs, fmt.Sprintf("%soption-%d-not-object", p, j))
				continue
			}
			mapKeys(o, map[string]bool{"id": true, "description": true}, fmt.Sprintf("%soption-%d-", p, j), &errs)
			id, _ := o["id"].(string)
			if strings.TrimSpace(id) == "" || !nonblank(o["description"]) || seenOption[id] {
				errs = append(errs, fmt.Sprintf("%soption-%d-invalid", p, j))
			}
			seenOption[id] = true
		}
	}
	return errs
}

func DecodeResult(m map[string]any) (ReviewResult, []string) {
	if errs := ValidateResult(m); len(errs) > 0 {
		return ReviewResult{}, errs
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ReviewResult{}, []string{"result-marshal:" + err.Error()}
	}
	var out ReviewResult
	if err := json.Unmarshal(b, &out); err != nil {
		return ReviewResult{}, []string{"result-decode:" + err.Error()}
	}
	return out, nil
}

// ValidateApprovalRequestInput applies the same open-ended typed-record
// contract to driver-created requests and reviewer-returned requests.
func ValidateApprovalRequestInput(r ApprovalRequestInput) error {
	if !namespacedType.MatchString(r.Type) || strings.TrimSpace(r.Scope) == "" || strings.TrimSpace(r.Reason) == "" {
		return fmt.Errorf("approval request type, scope, and reason are required; type must be namespaced")
	}
	if len(r.Options) == 0 {
		return fmt.Errorf("approval request requires at least one explicit option")
	}
	seen := map[string]bool{}
	for i, option := range r.Options {
		if strings.TrimSpace(option.ID) == "" || strings.TrimSpace(option.Description) == "" || seen[option.ID] {
			return fmt.Errorf("approval request option %d is empty or duplicated", i)
		}
		seen[option.ID] = true
	}
	return nil
}

func ClassifyOutcome(execState kernel.ExecutionState, validationErrs []string) Outcome {
	if execState != kernel.ExecSucceeded {
		return OutcomeFailed
	}
	if len(validationErrs) > 0 {
		return OutcomeNeedsInput
	}
	return OutcomeResultValid
}

func optionExists(options []ApprovalOption, id string) bool {
	for _, option := range options {
		if option.ID == id {
			return true
		}
	}
	return false
}

func ValidateCanonical(anchors []EvidenceAnchor, findings []Finding, requests []ApprovalRequest) error {
	anchorIDs := map[string]bool{}
	for _, a := range anchors {
		if strings.TrimSpace(a.ID) == "" || anchorIDs[a.ID] || strings.TrimSpace(a.Member) == "" ||
			!hexDigest.MatchString(a.MemberDigest) || !hexDigest.MatchString(a.AggregateRevision) || strings.TrimSpace(a.Claim) == "" {
			return fmt.Errorf("canonical evidence anchor %q invalid or duplicated: fail-closed", a.ID)
		}
		if a.Assurance != AssuranceContentMatch && a.Assurance != AssuranceContentMatchNormalized &&
			a.Assurance != AssuranceReviewerDeclared {
			return fmt.Errorf("canonical evidence anchor %s assurance %q invalid: fail-closed", a.ID, a.Assurance)
		}
		if (a.Assurance == AssuranceContentMatch || a.Assurance == AssuranceContentMatchNormalized) &&
			(a.LocationKind != "text-lines" || a.StartLine < 1 || a.EndLine < a.StartLine || a.Excerpt == "") {
			return fmt.Errorf("content-match anchor %s lacks a verified text range: fail-closed", a.ID)
		}
		if (a.Assurance == AssuranceContentMatch || a.Assurance == AssuranceContentMatchNormalized) &&
			(a.EndLine-a.StartLine+1 > MaxEvidenceLines || len(a.Excerpt) > MaxEvidenceBytes) {
			return fmt.Errorf("content-match anchor %s exceeds the bounded excerpt contract: fail-closed", a.ID)
		}
		if a.Assurance == AssuranceContentMatch && a.Normalization != "" {
			return fmt.Errorf("exact content-match anchor %s unexpectedly declares normalization: fail-closed", a.ID)
		}
		if a.Assurance == AssuranceContentMatchNormalized && a.Normalization != NormalizationCRLFToLF {
			return fmt.Errorf("normalized content-match anchor %s lacks the CRLF normalization fact: fail-closed", a.ID)
		}
		if a.Assurance == AssuranceReviewerDeclared &&
			((a.LocationKind != "opaque" && a.LocationKind != "empty-member") ||
				a.StartLine != 0 || a.EndLine != 0 || a.Excerpt != "" || a.Normalization != "") {
			return fmt.Errorf("reviewer-declared anchor %s is not a valid opaque/empty downgrade: fail-closed", a.ID)
		}
		anchorIDs[a.ID] = true
	}
	requestByID := map[string]ApprovalRequest{}
	for _, r := range requests {
		if strings.TrimSpace(r.ID) == "" || strings.TrimSpace(r.RequesterRole) == "" ||
			strings.TrimSpace(r.Requester) == "" || strings.TrimSpace(r.Scope) == "" || strings.TrimSpace(r.Reason) == "" ||
			len(r.Options) == 0 || !hexDigest.MatchString(r.TargetRevision) ||
			(r.Status != ApprovalOpen && r.Status != ApprovalResolved && r.Status != ApprovalWithdrawn) {
			return fmt.Errorf("approval request %q invalid: fail-closed", r.ID)
		}
		if err := ValidateApprovalRequestInput(ApprovalRequestInput{Type: r.Type, Scope: r.Scope, Reason: r.Reason, Options: r.Options}); err != nil {
			return fmt.Errorf("approval request %s invalid: %w", r.ID, err)
		}
		if _, exists := requestByID[r.ID]; exists {
			return fmt.Errorf("approval request %s duplicated: fail-closed", r.ID)
		}
		if r.Status == ApprovalResolved {
			if r.Response == nil || strings.TrimSpace(r.Response.Actor) == "" || strings.TrimSpace(r.Response.Verbatim) == "" ||
				!ValidResponseDate(r.Response.RespondedAt) || !optionExists(r.Options, r.Response.Decision) ||
				r.Response.DecisionScope != r.Scope || strings.TrimSpace(r.Response.DurableAnchor) == "" || !r.Response.Unambiguous || r.Stale {
				return fmt.Errorf("resolved approval request %s lacks an exact declared response: fail-closed", r.ID)
			}
		}
		requestByID[r.ID] = r
	}
	seenFinding := map[string]bool{}
	for _, f := range findings {
		if strings.TrimSpace(f.ID) == "" || seenFinding[f.ID] || !ValidSeverity(f.ReviewerSeverity) ||
			f.Blocking != BlockingForSeverity(f.ReviewerSeverity) || strings.TrimSpace(f.Summary) == "" ||
			strings.TrimSpace(f.Recommendation) == "" || len(f.Evidence) == 0 {
			return fmt.Errorf("canonical finding %q invalid: fail-closed", f.ID)
		}
		seenRef := map[string]bool{}
		for _, ref := range f.Evidence {
			if !anchorIDs[ref] {
				return fmt.Errorf("finding %s references unknown evidence %s: fail-closed", f.ID, ref)
			}
			if seenRef[ref] {
				return fmt.Errorf("finding %s duplicates evidence %s: fail-closed", f.ID, ref)
			}
			seenRef[ref] = true
		}
		if f.Disposition != "" {
			if !ValidDisposition(f.Disposition) || strings.TrimSpace(f.Rationale) == "" {
				return fmt.Errorf("finding %s disposition lacks a valid rationale: fail-closed", f.ID)
			}
			if (f.Disposition == DispositionAccept || f.Disposition == DispositionRevise) && strings.TrimSpace(f.FollowUp) == "" {
				return fmt.Errorf("finding %s %s disposition requires follow-up or explicit no-action: fail-closed", f.ID, f.Disposition)
			}
			if f.Disposition == DispositionNeedsUser {
				if _, ok := requestByID[f.ApprovalRequestID]; !ok {
					return fmt.Errorf("finding %s needs-user disposition lacks an approval request: fail-closed", f.ID)
				}
			} else if f.ApprovalRequestID != "" {
				return fmt.Errorf("finding %s non-needs-user disposition references an approval request: fail-closed", f.ID)
			}
		}
		seenFinding[f.ID] = true
	}
	return nil
}

func checkBlockingDispositions(findings []Finding, requests []ApprovalRequest, requireResolved bool) error {
	if err := ValidateCanonical(nil, nil, requests); err != nil && len(requests) > 0 {
		return err
	}
	requestByID := map[string]ApprovalRequest{}
	for _, r := range requests {
		requestByID[r.ID] = r
		if requireResolved && r.Status == ApprovalOpen {
			return fmt.Errorf("approval request %s remains open: governance stays decision-required", r.ID)
		}
	}
	for _, f := range findings {
		if !f.Blocking {
			continue
		}
		if f.Disposition == "" {
			return fmt.Errorf("blocking finding %s has no disposition: governance stays decision-required", f.ID)
		}
		if !ValidDisposition(f.Disposition) || strings.TrimSpace(f.Rationale) == "" {
			return fmt.Errorf("blocking finding %s has invalid or rationale-less disposition: fail-closed", f.ID)
		}
		if (f.Disposition == DispositionAccept || f.Disposition == DispositionRevise) && strings.TrimSpace(f.FollowUp) == "" {
			return fmt.Errorf("blocking finding %s %s disposition lacks follow-up: fail-closed", f.ID, f.Disposition)
		}
		if f.Disposition == DispositionNeedsUser {
			r, ok := requestByID[f.ApprovalRequestID]
			if !ok {
				return fmt.Errorf("blocking finding %s lacks an owner request reference: fail-closed", f.ID)
			}
			if requireResolved && r.Status != ApprovalResolved {
				return fmt.Errorf("blocking finding %s awaits a resolved owner request: governance stays decision-required", f.ID)
			}
		}
	}
	return nil
}

// ClosureCheckForClose requires every blocking finding to be dispositioned and
// every review-time approval request to be resolved or withdrawn.
func ClosureCheckForClose(findings []Finding, requests []ApprovalRequest) error {
	return checkBlockingDispositions(findings, requests, true)
}

// ClosureCheckForAdvance validates dispositions and request references but
// intentionally permits unresolved requests to carry to the next target
// revision, where the relay marks open requests stale.
func ClosureCheckForAdvance(findings []Finding, requests []ApprovalRequest) error {
	return checkBlockingDispositions(findings, requests, false)
}
