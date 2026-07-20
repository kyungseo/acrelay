package review

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/kernel"
)

func resultMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func hasError(errs []string, fragment string) bool {
	for _, err := range errs {
		if strings.Contains(err, fragment) {
			return true
		}
	}
	return false
}

func TestValidateResultV02(t *testing.T) {
	valid := resultMap(t, `{
		"verdict":"approve",
		"examined":[{"id":"E1","member":"target.go","location":{"kind":"text-lines","start":1,"end":1},"excerpt":"package target","claim":"read package declaration"}],
		"findings":[],
		"approval_requests":[]
	}`)
	if errs := ValidateResult(valid); len(errs) != 0 {
		t.Fatalf("valid v0.2 result rejected: %v", errs)
	}
	emptyMember := resultMap(t, `{
		"verdict":"approve",
		"examined":[{"id":"E1","member":"empty.txt","location":{"kind":"empty-member"},"claim":"examined empty member"}],
		"findings":[],
		"approval_requests":[]
	}`)
	if errs := ValidateResult(emptyMember); len(errs) != 0 {
		t.Fatalf("valid empty-member result rejected: %v", errs)
	}

	cases := []struct {
		name     string
		mutate   func(map[string]any)
		fragment string
	}{
		{"empty-attestation", func(m map[string]any) { m["examined"] = []any{} }, "examined-empty"},
		{"finding-needs-evidence", func(m map[string]any) {
			m["verdict"] = "changes-requested"
			m["findings"] = []any{map[string]any{"summary": "bug", "reviewer_severity": "high", "evidence": []any{}, "recommendation": "fix"}}
		}, "evidence-empty"},
		{"severity-enum", func(m map[string]any) {
			m["findings"] = []any{map[string]any{"summary": "bug", "reviewer_severity": "P1", "evidence": []any{"E1"}, "recommendation": "fix"}}
		}, "severity-enum:P1"},
		{"typed-request", func(m map[string]any) {
			m["approval_requests"] = []any{map[string]any{"type": "fixed-enum", "scope": "s", "reason": "r", "options": []any{map[string]any{"id": "yes", "description": "approve"}}}}
		}, "type-scope-or-reason-invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := resultMap(t, `{
				"verdict":"approve",
				"examined":[{"id":"E1","member":"target.go","location":{"kind":"text-lines","start":1,"end":1},"excerpt":"package target","claim":"read package declaration"}],
				"findings":[],"approval_requests":[]
			}`)
			tc.mutate(copy)
			if errs := ValidateResult(copy); !hasError(errs, tc.fragment) {
				t.Fatalf("errors %v do not contain %q", errs, tc.fragment)
			}
		})
	}
}

func TestClassifyOutcome(t *testing.T) {
	if o := ClassifyOutcome(kernel.ExecSucceeded, nil); o != OutcomeResultValid {
		t.Fatalf("got %s", o)
	}
	if o := ClassifyOutcome(kernel.ExecSucceeded, []string{"excerpt-mismatch"}); o != OutcomeNeedsInput {
		t.Fatalf("needs-input is a dispatch outcome, got %s", o)
	}
	if o := ClassifyOutcome(kernel.ExecFailed, nil); o != OutcomeFailed {
		t.Fatalf("got %s", o)
	}
}

func canonicalFixture() ([]EvidenceAnchor, []Finding, []ApprovalRequest) {
	digest := strings.Repeat("a", 64)
	aggregate := strings.Repeat("b", 64)
	anchors := []EvidenceAnchor{{
		ID: "R0-E1", Round: 0, Member: "target.go", MemberDigest: digest,
		AggregateRevision: aggregate, LocationKind: "text-lines", StartLine: 1, EndLine: 1,
		Excerpt: "package target", Claim: "read package", Assurance: AssuranceContentMatch,
	}}
	findings := []Finding{{
		ID: "R0-F1", ReviewerSeverity: SeverityHigh, Blocking: true, Summary: "bug",
		Evidence: []string{"R0-E1"}, Recommendation: "fix",
	}}
	requests := []ApprovalRequest{{
		ID: "AR-1", Type: "review.risk-acceptance", RequesterRole: "driver", Requester: "codex",
		Scope: "R0-F1 only", Reason: "owner decision required",
		Options: []ApprovalOption{{ID: "accept", Description: "accept risk"}},
		Status:  ApprovalOpen, TargetRevision: aggregate,
	}}
	return anchors, findings, requests
}

func TestCanonicalDispositionAndApprovalFailClosed(t *testing.T) {
	anchors, findings, requests := canonicalFixture()
	if err := ValidateCanonical(anchors, findings, requests); err != nil {
		t.Fatal(err)
	}
	if err := ClosureCheckForClose(findings, requests); err == nil {
		t.Fatal("open request and undispositioned blocking finding must block closure")
	}
	findings[0].Disposition = DispositionNeedsUser
	findings[0].Rationale = "owner must choose"
	findings[0].ApprovalRequestID = "AR-1"
	if err := ValidateCanonical(anchors, findings, requests); err != nil {
		t.Fatalf("open request must be persistable: %v", err)
	}
	if err := ClosureCheckForClose(findings, requests); err == nil {
		t.Fatal("open request must still block Close")
	}
	if err := ClosureCheckForAdvance(findings, requests); err != nil {
		t.Fatalf("open referenced request must be allowed to carry on Advance: %v", err)
	}
	requests[0].Status = ApprovalResolved
	requests[0].Response = &OwnerResponse{
		Actor: "owner", Verbatim: "accept", RespondedAt: "2026-07-20", Decision: "accept",
		DecisionScope: "R0-F1 only", DurableAnchor: "canonical#approval-response-AR-1", Unambiguous: true,
	}
	if err := ValidateCanonical(anchors, findings, requests); err != nil {
		t.Fatal(err)
	}
	if err := ClosureCheckForClose(findings, requests); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalEvidenceAssuranceSeparation(t *testing.T) {
	anchors, _, _ := canonicalFixture()
	anchors[0].Assurance = AssuranceContentMatchNormalized
	anchors[0].Normalization = NormalizationCRLFToLF
	if err := ValidateCanonical(anchors, nil, nil); err != nil {
		t.Fatalf("normalized CRLF match rejected: %v", err)
	}

	anchors[0].Normalization = ""
	if err := ValidateCanonical(anchors, nil, nil); err == nil {
		t.Fatal("normalized assurance without its normalization fact must fail")
	}

	anchors[0].Assurance = AssuranceContentMatch
	anchors[0].Normalization = NormalizationCRLFToLF
	if err := ValidateCanonical(anchors, nil, nil); err == nil {
		t.Fatal("exact assurance must not retain a normalization fact")
	}

	anchors[0].Assurance = AssuranceReviewerDeclared
	anchors[0].Normalization = ""
	anchors[0].LocationKind = "empty-member"
	anchors[0].StartLine = 0
	anchors[0].EndLine = 0
	anchors[0].Excerpt = ""
	if err := ValidateCanonical(anchors, nil, nil); err != nil {
		t.Fatalf("empty-member reviewer declaration rejected: %v", err)
	}
}

func TestBlockingPolicyOwnedByRuntime(t *testing.T) {
	for severity, want := range map[string]bool{
		SeverityCritical: true, SeverityHigh: true, SeverityMedium: false, SeverityLow: false,
	} {
		if got := BlockingForSeverity(severity); got != want {
			t.Fatalf("severity %s blocking=%v want %v", severity, got, want)
		}
	}
}
