package adapter

import "fmt"

// Failure-cause registry (FEAT-20260721-002). Causes are orthogonal owner
// remediation diagnostics: they never change the execution state machine and
// never authorize an automatic retry. The registry is a bounded set with an
// explicit unknown fallback — arbitrary open-ended codes are rejected so a
// vendor-text inference can never masquerade as a new verified category.
const FailureCauseVersion = "failure-cause v0.1"

// Cause source states. "observed" is a runtime-verified fact (exit status,
// signal, timeout). "vendor-declared" comes from a typed vendor event field.
// "inferred" was classified from vendor text and is never a verified fact.
const (
	CauseSourceObserved       = "observed"
	CauseSourceVendorDeclared = "vendor-declared"
	CauseSourceInferred       = "inferred"
)

// FailureCause carries only a registry code and its source. Raw vendor text,
// paths, and diagnostics never enter this struct; human phrasing is generated
// from the code via CausePhrase (allowlist, R0-CX-F6).
type FailureCause struct {
	Code   string `json:"code"`
	Source string `json:"source"`
}

const (
	CauseTimeoutStartup           = "timeout.startup"
	CauseTimeoutIdle              = "timeout.idle"
	CauseTimeoutHardCap           = "timeout.hard-cap"
	CauseTerminatedSignal         = "terminated.signal"
	CauseCanceledParentSignal     = "canceled.parent-signal"
	CauseMissingTerminal          = "transport.missing-terminal"
	CauseMalformedTerminal        = "transport.malformed-terminal"
	CauseVendorTurnFailed         = "vendor.turn-failed"
	CauseVendorErrorEnvelope      = "vendor.error-envelope"
	CauseNoStructuredOutput       = "contract.no-structured-output"
	CauseResumeHandleInvalid      = "resume-handle-invalid"
	CauseJournalReconciledUnknown = "journal.reconciled-unknown"
	CauseUnknown                  = "unknown"
	// Owner-remediation categories (absorbed dispatch-failure-taxonomy SSoT).
	// Neither vendor contract currently exposes a typed error-category field,
	// so assigning these requires vendor-declared evidence (none today) or
	// version-bound inferred text signatures acquired from live observation.
	// Until such evidence exists the runtime records the mechanism cause or
	// `unknown` — it never guesses a remediation category (R1-CX-F5, AR-3).
	CauseVendorQuota              = "vendor.quota"
	CauseVendorAuth               = "vendor.auth"
	CauseVendorNetwork            = "vendor.network"
	CauseVendorServiceUnavailable = "vendor.service-unavailable"
	CauseVendorToolPolicy         = "vendor.tool-policy"
)

// causeRegistry binds every code to its allowlisted phrase AND its allowed
// source states (R1-CX-F3): a persisted `inferred` classification can never
// be upgraded to `observed` without failing the load gate.
type causeSpec struct {
	phrase  string
	sources map[string]bool
}

func obs() map[string]bool      { return map[string]bool{CauseSourceObserved: true} }
func declared() map[string]bool { return map[string]bool{CauseSourceVendorDeclared: true} }
func declaredOrInferred() map[string]bool {
	return map[string]bool{CauseSourceVendorDeclared: true, CauseSourceInferred: true}
}

var causeRegistry = map[string]causeSpec{
	CauseTimeoutStartup:       {"the reviewer produced no observable output before the startup timeout", obs()},
	CauseTimeoutIdle:          {"the reviewer stream went silent past the idle timeout", obs()},
	CauseTimeoutHardCap:       {"the hard-cap timeout cut the invocation mid-flight", obs()},
	CauseTerminatedSignal:     {"the reviewer process was terminated by a signal before any terminal output", obs()},
	CauseCanceledParentSignal: {"the acrelay parent received a termination signal and canceled the dispatch", obs()},
	CauseMissingTerminal:      {"the reviewer exited cleanly without the required terminal contract output", obs()},
	CauseMalformedTerminal:    {"the reviewer terminal output did not parse under the adapter contract", obs()},
	CauseVendorTurnFailed:     {"the vendor reported a failed turn", declared()},
	CauseVendorErrorEnvelope:  {"the vendor terminal envelope reported an error", declared()},
	CauseNoStructuredOutput:   {"the reviewer completed without structured output", obs()},
	CauseResumeHandleInvalid:  {"the vendor rejected the stored session handle; an explicit session reset is required", declaredOrInferred()},
	CauseJournalReconciledUnknown: {
		"a prepared dispatch journal was reconciled without a captured result; the underlying interruption cause is unverified", obs()},
	CauseUnknown:                  {"no verified cause classification is available", obs()},
	CauseVendorQuota:              {"the vendor reported a quota or rate limit; wait or adjust usage before a new objective", declaredOrInferred()},
	CauseVendorAuth:               {"the vendor reported an authentication or permission failure; fix credentials before a new objective", declaredOrInferred()},
	CauseVendorNetwork:            {"a network failure prevented the vendor exchange; check connectivity before a new objective", declaredOrInferred()},
	CauseVendorServiceUnavailable: {"the vendor service was unavailable; retry later via a new objective", declaredOrInferred()},
	CauseVendorToolPolicy:         {"the vendor denied a tool or policy request; adjust the profile before a new objective", declaredOrInferred()},
}

// ValidCause reports whether a persisted cause uses a registry code with a
// source state that code is allowed to claim (exact pair validation).
func ValidCause(c FailureCause) error {
	spec, ok := causeRegistry[c.Code]
	if !ok {
		return fmt.Errorf("failure cause code %q is not in the %s registry: fail-closed", c.Code, FailureCauseVersion)
	}
	if !spec.sources[c.Source] {
		return fmt.Errorf("failure cause %q does not permit source %q: fail-closed", c.Code, c.Source)
	}
	return nil
}

// CausePhrase returns the allowlisted human phrase for a registry code.
func CausePhrase(code string) string {
	if spec, ok := causeRegistry[code]; ok {
		return spec.phrase
	}
	return "unrecognized cause code"
}

// Termination is the adapter's typed end-of-dispatch classification input for
// the relay (FEAT-20260721-002 precedence table). Ambiguous means the adapter
// cannot assert whether vendor-side execution completed — the relay maps it to
// UNKNOWN with no automatic retry. A nil Cause is permitted only on success.
type Termination struct {
	Ambiguous bool
	Cause     *FailureCause
}

func observedCause(code string) *FailureCause {
	return &FailureCause{Code: code, Source: CauseSourceObserved}
}
