package relay

import (
	"fmt"
	"strings"
)

// TopologyVersion is the review-topology v0.1 contract: reviewer topology is
// recorded as source-qualified relation facets, never as a verified
// independence claim. Profile names are derived labels only (FEAT-20260722-001
// AR-1). The policy is objective-immutable: it is fixed at init and a changed
// topology relation requires a new objective (AR-2).
const TopologyVersion = "review-topology v0.1"

// Facet sources. Every projected facet carries exactly one source qualifier so
// an operator declaration is never displayed as a runtime observation.
const (
	FacetOperatorDeclared  = "operator-declared"
	FacetRuntimeObserved   = "runtime-observed"
	FacetDerivedUnverified = "derived-not-verified"
	FacetUnknownUndeclared = "unknown-undeclared"
)

// Execution surfaces. The standalone CLI always runs the reviewer leg as an
// external vendor CLI child; a host-orchestrated self-subagent surface has no
// stable capability and is explicitly unsupported (AR-3) — never a silent
// fallback to another topology.
const (
	SurfaceExternalCLI  = "external-cli"
	SurfaceHostSubagent = "host-subagent"
)

// Operator-declared facet values. "undeclared" is an explicit recorded value:
// an omitted declaration never upgrades to any topology claim.
const (
	TopologyUndeclared = "undeclared"
	DriverVendorOther  = "other"
	ContextSeparate    = "separate"
	ContextShared      = "shared"
	SessionModeNew     = "new"
	SessionModeResumed = "resumed"
	SessionModeReset   = "reset"
	SessionModeCarried = "carried" // session carried from a related objective, not yet dispatched
	SessionModeNone    = "none"
	// SessionModeUnknown is the conservative downgrade (R1-CX-F1): a dispatch
	// was started but its session outcome cannot be verified (e.g. a reset
	// that returned no ref, or a prepared-journal UNKNOWN reconcile). It keeps
	// the resumed/non-fresh caution rather than presenting a stale mode.
	SessionModeUnknown     = "unknown"
	RelationSameVendor     = "same-vendor"
	RelationCrossVendor    = "cross-vendor"
	RelationUnknown        = "unknown"
	ProfileCrossVendorExt  = "cross-vendor-external"
	ProfileSameVendorExt   = "same-vendor-external"
	ProfileUndeclared      = "undeclared"
	SeparationDeclaredOnly = "declared-not-verified"
	SeparationSharedDecl   = "declared-shared"
	SeparationUnknown      = "unknown"
)

// TopologyPolicy is the objective-immutable operator declaration. It records
// what the operator claims about the driver side; acrelay cannot observe the
// driver, so these facts are provenance, never verification. Runtime facts
// (reviewer vendor, reviewer session mode) live on State and are recorded at
// dispatch without being a policy change (AR-2).
type TopologyPolicy struct {
	Version          string `json:"version"`
	ExecutionSurface string `json:"execution_surface"`
	DriverVendor     string `json:"driver_vendor"`
	ContextRelation  string `json:"context_relation"`
}

// ErrSelfSubagentUnsupported is the actionable fail-closed diagnostic for the
// deferred self-subagent topology (AR-3). It must name what is unsupported,
// why, and the supported alternatives — and must never fall back silently.
var ErrSelfSubagentUnsupported = fmt.Errorf(
	"self-subagent topology is not supported: this standalone CLI dispatches the reviewer leg only as an external vendor CLI child and has no typed ingress for host-orchestrated subagent results. " +
		"Declare an external reviewer instead (-driver-vendor claude|codex|other, -context-relation separate|shared) for a cross-vendor or same-vendor-external topology. " +
		"Host subagent review is deferred until a stable host API and a typed external-result ingress contract exist: fail-closed, no fallback")

// NewTopologyPolicy validates operator flags into the immutable policy.
// Omitted values record the explicit "undeclared" fact.
func NewTopologyPolicy(surface, driverVendor, contextRelation string) (TopologyPolicy, error) {
	p := TopologyPolicy{Version: TopologyVersion}
	switch strings.TrimSpace(surface) {
	case "", SurfaceExternalCLI:
		p.ExecutionSurface = SurfaceExternalCLI
	case SurfaceHostSubagent:
		return TopologyPolicy{}, ErrSelfSubagentUnsupported
	default:
		return TopologyPolicy{}, fmt.Errorf("execution surface %q unsupported (external-cli; host-subagent is explicitly unsupported): fail-closed", surface)
	}
	switch strings.TrimSpace(driverVendor) {
	case "":
		p.DriverVendor = TopologyUndeclared
	case "claude", "codex", DriverVendorOther:
		p.DriverVendor = strings.TrimSpace(driverVendor)
	default:
		return TopologyPolicy{}, fmt.Errorf("driver vendor %q invalid (claude|codex|other, omitted records undeclared): fail-closed", driverVendor)
	}
	switch strings.TrimSpace(contextRelation) {
	case "":
		p.ContextRelation = TopologyUndeclared
	case ContextSeparate, ContextShared:
		p.ContextRelation = strings.TrimSpace(contextRelation)
	default:
		return TopologyPolicy{}, fmt.Errorf("context relation %q invalid (separate|shared, omitted records undeclared): fail-closed", contextRelation)
	}
	return p, nil
}

// DefaultTopologyPolicy is the fully undeclared policy: no topology claim can
// be derived from it.
func DefaultTopologyPolicy() TopologyPolicy {
	return TopologyPolicy{
		Version:          TopologyVersion,
		ExecutionSurface: SurfaceExternalCLI,
		DriverVendor:     TopologyUndeclared,
		ContextRelation:  TopologyUndeclared,
	}
}

// Validate rejects forged or drifted persisted policies. host-subagent never
// persists: it is rejected at declaration time.
func (p TopologyPolicy) Validate() error {
	if p.Version != TopologyVersion {
		return fmt.Errorf("topology version %q unsupported (want %q): fail-closed", p.Version, TopologyVersion)
	}
	if p.ExecutionSurface != SurfaceExternalCLI {
		return fmt.Errorf("persisted execution surface %q invalid: fail-closed", p.ExecutionSurface)
	}
	switch p.DriverVendor {
	case "claude", "codex", DriverVendorOther, TopologyUndeclared:
	default:
		return fmt.Errorf("persisted driver vendor %q invalid: fail-closed", p.DriverVendor)
	}
	switch p.ContextRelation {
	case ContextSeparate, ContextShared, TopologyUndeclared:
	default:
		return fmt.Errorf("persisted context relation %q invalid: fail-closed", p.ContextRelation)
	}
	return nil
}

// TopologyFacet is one source-qualified relation fact.
type TopologyFacet struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

func declaredSource(value string) string {
	if value == TopologyUndeclared {
		return FacetUnknownUndeclared
	}
	return FacetOperatorDeclared
}

// vendorRelation derives the driver/reviewer vendor relation from the declared
// driver vendor and the runtime-observed reviewer vendor. Any missing side
// yields unknown — never an upgraded claim.
func vendorRelation(declaredDriver, observedReviewer string) (string, string) {
	if declaredDriver == TopologyUndeclared || observedReviewer == "" {
		return RelationUnknown, FacetUnknownUndeclared
	}
	if declaredDriver == observedReviewer {
		return RelationSameVendor, FacetDerivedUnverified
	}
	return RelationCrossVendor, FacetDerivedUnverified
}

// reviewerSessionModeFact projects the runtime session-mode fact: an actual
// dispatch observation wins; a session carried from a related objective before
// any dispatch is "carried"; otherwise no session exists yet.
func reviewerSessionModeFact(st *State) string {
	if st.ReviewerSessionMode != "" {
		return st.ReviewerSessionMode
	}
	if st.SessionRef != "" {
		return SessionModeCarried
	}
	return SessionModeNone
}

// TopologyFacets projects the six source-qualified facets (AR-1) from the
// immutable policy plus the runtime facts on State.
func TopologyFacets(st *State) []TopologyFacet {
	p := st.Topology
	if p == nil {
		return nil
	}
	reviewerVendor, reviewerSource := st.Vendor, FacetRuntimeObserved
	if reviewerVendor == "" {
		reviewerVendor, reviewerSource = "none", FacetUnknownUndeclared
	}
	relation, relationSource := vendorRelation(p.DriverVendor, st.Vendor)
	sessionMode, sessionSource := reviewerSessionModeFact(st), FacetRuntimeObserved
	if sessionMode == SessionModeUnknown {
		sessionSource = FacetUnknownUndeclared
	}
	return []TopologyFacet{
		// execution_surface is part of the operator-declared init policy
		// (DR-814 §B) — the CLI validates it but does not observe the driver
		// side, so it is never presented as a runtime observation (R1-CX-F2).
		{Name: "execution_surface", Value: p.ExecutionSurface, Source: FacetOperatorDeclared},
		{Name: "driver_vendor", Value: p.DriverVendor, Source: declaredSource(p.DriverVendor)},
		{Name: "reviewer_vendor", Value: reviewerVendor, Source: reviewerSource},
		{Name: "vendor_relation", Value: relation, Source: relationSource},
		{Name: "reviewer_session_mode", Value: sessionMode, Source: sessionSource},
		{Name: "context_relation", Value: p.ContextRelation, Source: declaredSource(p.ContextRelation)},
	}
}

// DerivedTopologyProfile maps the facets onto the derived label. It is a
// display name only and asserts no verified independence (AR-1).
func DerivedTopologyProfile(st *State) string {
	if st.Topology == nil {
		return ProfileUndeclared
	}
	relation, _ := vendorRelation(st.Topology.DriverVendor, st.Vendor)
	switch relation {
	case RelationCrossVendor:
		return ProfileCrossVendorExt
	case RelationSameVendor:
		return ProfileSameVendorExt
	}
	return ProfileUndeclared
}

// DriverSessionSeparation projects the separation claim state (R0-CX-F2):
// a declared separation is only ever "declared-not-verified".
func DriverSessionSeparation(st *State) string {
	if st.Topology == nil {
		return SeparationUnknown
	}
	switch st.Topology.ContextRelation {
	case ContextSeparate:
		return SeparationDeclaredOnly
	case ContextShared:
		return SeparationSharedDecl
	}
	return SeparationUnknown
}
