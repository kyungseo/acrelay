// Package review implements the v1 review profile: rounds, findings,
// dispositions, the canonical ReviewResult contract (DR-811 §5), and the
// fail-closed closure check (DR-811 §6).
package review

import (
	"fmt"
	"sort"

	"github.com/kyungseo/acrelay/internal/kernel"
)

// Verdict is the reviewer's formal judgment. needs-input is deliberately NOT
// a verdict: it is a dispatch outcome (see Outcome).
type Verdict string

const (
	VerdictApprove          Verdict = "approve"
	VerdictChangesRequested Verdict = "changes-requested"
)

// Outcome is the dispatch-level classification of one reviewer invocation.
type Outcome string

const (
	OutcomeResultValid Outcome = "result-valid"
	OutcomeNeedsInput  Outcome = "needs-input" // structured result failed validation
	OutcomeFailed      Outcome = "failed"
)

// Disposition is the driver's response to a finding.
type Disposition string

const (
	DispositionAccept    Disposition = "accept"
	DispositionRevise    Disposition = "revise"
	DispositionDefend    Disposition = "defend"
	DispositionNeedsUser Disposition = "needs-user"
)

// Finding is one reviewer finding. Severity is impact; Blocking is whether
// current policy blocks closure — the two are never conflated.
type Finding struct {
	ID          string
	Severity    string
	Blocking    bool
	Summary     string
	Disposition Disposition // empty until the driver responds
	Decided     bool        // arbiter decision recorded for needs-user
}

// ReviewResult is the canonical structured reviewer output.
type ReviewResult struct {
	Verdict  Verdict  `json:"verdict"`
	Findings []string `json:"findings"`
}

// ValidateResult enforces the canonical ReviewResult semantics. The error
// codes match the cross-runtime parity fixtures from FEAT-20260718-001.
func ValidateResult(m map[string]any) []string {
	var errs []string
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != "verdict" && k != "findings" {
			errs = append(errs, "unknown-property:"+k)
		}
	}
	verdict, hasVerdict := m["verdict"].(string)
	if _, ok := m["verdict"]; !ok {
		errs = append(errs, "missing:verdict")
	} else if !hasVerdict || (verdict != string(VerdictApprove) && verdict != string(VerdictChangesRequested)) {
		errs = append(errs, fmt.Sprintf("verdict-enum:%v", m["verdict"]))
	}
	rawFindings, ok := m["findings"]
	if !ok {
		errs = append(errs, "missing:findings")
		return errs
	}
	arr, isArr := rawFindings.([]any)
	if !isArr {
		errs = append(errs, "findings-not-array")
		return errs
	}
	for i, f := range arr {
		s, isStr := f.(string)
		if !isStr || s == "" {
			errs = append(errs, fmt.Sprintf("empty-finding:%d", i))
		}
	}
	if verdict == string(VerdictChangesRequested) && len(arr) == 0 {
		errs = append(errs, "changes-requested-needs-finding")
	}
	return errs
}

// ClassifyOutcome maps an execution result plus validation errors to the
// dispatch outcome layer.
func ClassifyOutcome(execState kernel.ExecutionState, validationErrs []string) Outcome {
	if execState != kernel.ExecSucceeded {
		return OutcomeFailed
	}
	if len(validationErrs) > 0 {
		return OutcomeNeedsInput
	}
	return OutcomeResultValid
}

// ClosureCheck is the post-R2 fail-closed gate (DR-811 §6): every blocking
// finding needs a recorded disposition, and needs-user dispositions need an
// arbiter decision. There is no escape hatch.
func ClosureCheck(findings []Finding) error {
	for _, f := range findings {
		if !f.Blocking {
			continue
		}
		if f.Disposition == "" {
			return fmt.Errorf("blocking finding %s has no disposition: governance stays decision-required", f.ID)
		}
		if f.Disposition == DispositionNeedsUser && !f.Decided {
			return fmt.Errorf("blocking finding %s awaits arbiter decision: governance stays decision-required", f.ID)
		}
	}
	return nil
}
