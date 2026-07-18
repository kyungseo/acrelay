// Package review implements the v1 review profile: rounds, findings,
// dispositions, the canonical ReviewResult contract (DR-811 §5), and the
// fail-closed closure check (DR-811 §6).
package review

import (
	"fmt"
	"sort"
	"strings"

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

// ValidDisposition reports whether d is one of the contract's dispositions.
func ValidDisposition(d Disposition) bool {
	switch d {
	case DispositionAccept, DispositionRevise, DispositionDefend, DispositionNeedsUser:
		return true
	}
	return false
}

// ArbiterDecision records who decided a needs-user finding and why. A bare
// boolean is not evidence (R0-CX-F4).
type ArbiterDecision struct {
	Arbiter string
	Reason  string
}

// Validate rejects decisions without an identity or rationale.
func (d ArbiterDecision) Validate() error {
	if strings.TrimSpace(d.Arbiter) == "" || strings.TrimSpace(d.Reason) == "" {
		return fmt.Errorf("arbiter decision requires identity and reason: fail-closed")
	}
	return nil
}

// Finding is one reviewer finding. Severity is impact; Blocking is whether
// current policy blocks closure — the two are never conflated.
type Finding struct {
	ID          string
	Severity    string
	Blocking    bool
	Summary     string
	Disposition Disposition      // empty until the driver responds
	Decision    *ArbiterDecision // required when Disposition is needs-user
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
		if !isStr || strings.TrimSpace(s) == "" {
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
		if !ValidDisposition(f.Disposition) {
			return fmt.Errorf("blocking finding %s has invalid disposition %q: fail-closed", f.ID, f.Disposition)
		}
		if f.Disposition == DispositionNeedsUser {
			if f.Decision == nil {
				return fmt.Errorf("blocking finding %s awaits arbiter decision: governance stays decision-required", f.ID)
			}
			if err := f.Decision.Validate(); err != nil {
				return fmt.Errorf("blocking finding %s: %w", f.ID, err)
			}
		}
	}
	return nil
}
