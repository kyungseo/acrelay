package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/store"
	"github.com/kyungseo/acrelay/internal/subject"
)

var errFakeDispatch = errors.New("fake dispatch failure")

// newTopologySession is newSession with an explicit topology declaration.
func newTopologySession(t *testing.T, vendorName string, topology TopologyPolicy, script []adapter.FakeResult) (*Session, *adapter.FakeAdapter, string) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.go")
	if err := os.WriteFile(target, []byte("func greet() string { return \"hello\" }"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The native-handle format is vendor-bound (handles.go): claude requires
	// the observed UUID form.
	handle := "native-fixture-1"
	if vendorName == "claude" {
		handle = "01234567-89ab-cdef-0123-456789abcdef"
	}
	fake := &adapter.FakeAdapter{VendorName: vendorName, NativeHandle: handle, Script: script}
	s := &Session{
		Adapter:   fake,
		Handles:   &adapter.HandleStore{Path: filepath.Join(dir, "handles.json")},
		Canonical: filepath.Join(dir, "canonical.md"),
	}
	spec, err := subject.SingleFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitSubjectTopology(s.Canonical, "is hello ok?", spec, "", "", false, approvedPolicy(t), topology); err != nil {
		t.Fatal(err)
	}
	return s, fake, dir
}

func mustTopology(t *testing.T, surface, driver, context string) TopologyPolicy {
	t.Helper()
	p, err := NewTopologyPolicy(surface, driver, context)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func facetValue(facets []TopologyFacet, name string) (string, string) {
	for _, f := range facets {
		if f.Name == name {
			return f.Value, f.Source
		}
	}
	return "", ""
}

// AR-1: declaration validation — omitted values record the explicit
// undeclared fact; invalid enums fail closed.
func TestNewTopologyPolicyValidation(t *testing.T) {
	p := mustTopology(t, "", "", "")
	if p.ExecutionSurface != SurfaceExternalCLI || p.DriverVendor != TopologyUndeclared || p.ContextRelation != TopologyUndeclared {
		t.Fatalf("omitted declarations must record undeclared: %+v", p)
	}
	if _, err := NewTopologyPolicy("", "gpt", ""); err == nil {
		t.Fatal("invalid driver vendor must fail closed")
	}
	if _, err := NewTopologyPolicy("", "", "correlated"); err == nil {
		t.Fatal("invalid context relation must fail closed")
	}
	if _, err := NewTopologyPolicy("container", "", ""); err == nil {
		t.Fatal("unknown execution surface must fail closed")
	}
}

// AR-3: the self-subagent surface is explicitly unsupported with an
// actionable diagnostic and no silent fallback to another topology.
func TestSelfSubagentSurfaceUnsupportedFailClosed(t *testing.T) {
	_, err := NewTopologyPolicy(SurfaceHostSubagent, "claude", "shared")
	if !errors.Is(err, ErrSelfSubagentUnsupported) {
		t.Fatalf("host-subagent must fail with the typed unsupported error, got: %v", err)
	}
	for _, needle := range []string{"not supported", "external vendor CLI", "deferred", "no fallback", "-driver-vendor"} {
		if !strings.Contains(err.Error(), needle) {
			t.Fatalf("diagnostic must be actionable (missing %q): %v", needle, err)
		}
	}
	// Persisting the surface by hand fails validation too.
	forged := DefaultTopologyPolicy()
	forged.ExecutionSurface = SurfaceHostSubagent
	if err := forged.Validate(); err == nil {
		t.Fatal("persisted host-subagent surface must fail closed")
	}
}

// store-md v0.9: a state without the topology policy fails at the load
// boundary; a wrong topology version fails closed.
func TestStateRequiresTopologyPolicy(t *testing.T) {
	st := bindValidSubject(t, &State{
		Seq: 1, KernelVersion: KernelVersion, ProfileVersion: ProfileVersion, StoreVersion: StoreVersion,
		CollaborationID: "c", ObjectiveID: "o", Governance: "OPEN",
	})
	st.Topology = nil
	if err := validateState(st, 1); err == nil || !strings.Contains(err.Error(), "review-topology") {
		t.Fatalf("missing topology must fail closed, got: %v", err)
	}
	drifted := bindValidSubject(t, &State{
		Seq: 1, KernelVersion: KernelVersion, ProfileVersion: ProfileVersion, StoreVersion: StoreVersion,
		CollaborationID: "c", ObjectiveID: "o", Governance: "OPEN",
	})
	drifted.Topology.Version = "review-topology v0.0"
	if err := validateState(drifted, 1); err == nil {
		t.Fatal("topology version drift must fail closed")
	}
	badMode := bindValidSubject(t, &State{
		Seq: 1, KernelVersion: KernelVersion, ProfileVersion: ProfileVersion, StoreVersion: StoreVersion,
		CollaborationID: "c", ObjectiveID: "o", Governance: "OPEN",
		SessionRef: "sref-x", Vendor: "fake", ReviewerSessionMode: "fresh",
	})
	if err := validateState(badMode, 1); err == nil {
		t.Fatal("invalid reviewer session mode must fail closed")
	}
	orphanMode := bindValidSubject(t, &State{
		Seq: 1, KernelVersion: KernelVersion, ProfileVersion: ProfileVersion, StoreVersion: StoreVersion,
		CollaborationID: "c", ObjectiveID: "o", Governance: "OPEN",
		ReviewerSessionMode: SessionModeNew,
	})
	if err := validateState(orphanMode, 1); err == nil {
		t.Fatal("session mode without session ref must fail closed")
	}
}

// store-md v0.10 is an exact cutover: a digest-valid v0.9 state block is
// rejected at the load boundary, never migrated.
func TestLoadRejectsStoreV08Exact(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "c.md")
	st := bindValidSubject(t, &State{
		Seq: 0, KernelVersion: KernelVersion, ProfileVersion: ProfileVersion, StoreVersion: "store-md v0.9",
		CollaborationID: "collab-x", ObjectiveID: "obj-x", Question: "q",
		Governance: "OPEN",
	})
	block, err := stateSection(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(canonical); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("v0.8 canonical must be rejected exactly, got: %v", err)
	}
}

// AR-1: facet projection is source-qualified and never upgrades an
// undeclared value to a topology claim.
func TestTopologyFacetProjection(t *testing.T) {
	// Undeclared: unknown relation, undeclared profile.
	s, _, _ := newTopologySession(t, "fake", DefaultTopologyPolicy(), []adapter.FakeResult{approve()})
	if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got := DerivedTopologyProfile(st); got != ProfileUndeclared {
		t.Fatalf("undeclared driver must derive undeclared profile, got %s", got)
	}
	facets := TopologyFacets(st)
	if v, src := facetValue(facets, "vendor_relation"); v != RelationUnknown || src != FacetUnknownUndeclared {
		t.Fatalf("undeclared relation must be unknown/unknown-undeclared, got %s/%s", v, src)
	}
	if v, src := facetValue(facets, "reviewer_vendor"); v != "fake" || src != FacetRuntimeObserved {
		t.Fatalf("reviewer vendor must be runtime-observed, got %s/%s", v, src)
	}
	if v, src := facetValue(facets, "reviewer_session_mode"); v != SessionModeNew || src != FacetRuntimeObserved {
		t.Fatalf("first dispatch must record session mode new, got %s/%s", v, src)
	}

	// Same-vendor: declared driver equals the observed reviewer vendor.
	sv, _, _ := newTopologySession(t, "claude", mustTopology(t, "", "claude", "separate"), []adapter.FakeResult{approve()})
	if _, _, err := sv.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	svState, err := LoadState(sv.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got := DerivedTopologyProfile(svState); got != ProfileSameVendorExt {
		t.Fatalf("same declared/observed vendor must derive same-vendor-external, got %s", got)
	}
	if v, src := facetValue(TopologyFacets(svState), "vendor_relation"); v != RelationSameVendor || src != FacetDerivedUnverified {
		t.Fatalf("same-vendor relation must be derived-not-verified, got %s/%s", v, src)
	}
	if got := DriverSessionSeparation(svState); got != SeparationDeclaredOnly {
		t.Fatalf("declared separation is only ever declared-not-verified, got %s", got)
	}

	// Cross-vendor: declared driver differs from the observed reviewer.
	cv, _, _ := newTopologySession(t, "codex", mustTopology(t, "", "claude", "separate"), []adapter.FakeResult{approve()})
	if _, _, err := cv.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	cvState, err := LoadState(cv.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got := DerivedTopologyProfile(cvState); got != ProfileCrossVendorExt {
		t.Fatalf("differing vendors must derive cross-vendor-external, got %s", got)
	}
}

// R0-CX-F2 / AR-4 surfacing: same-vendor and undeclared topologies and
// resumed sessions are cautions in the briefing — never silently omitted,
// never a blocker upgrade.
func TestBriefingTopologyCautions(t *testing.T) {
	// Same-vendor with a resumed session: both cautions present.
	s, _, _ := newTopologySession(t, "claude", mustTopology(t, "", "claude", "separate"),
		[]adapter.FakeResult{changesRequested("finding one"), confirmationResult("R0-F1", "confirmed")})
	if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Disposition(s.Canonical, "R0-F1", acceptDisposition()); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfirmation(s.Canonical, 0, []string{"R0-F1"}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ConfirmWithReviewer(context.Background(), 0, st.TargetRevision, []string{"R0-F1"}, "delta", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	b, err := BuildBriefing(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !briefingHasReason(b.Cautions, "same-vendor-review") {
		t.Fatalf("same-vendor topology must surface the correlated-blind-spot caution: %+v", b.Cautions)
	}
	if !briefingHasReason(b.Cautions, "reviewer-session-resumed") {
		t.Fatalf("resumed reviewer session must surface the non-fresh-context caution: %+v", b.Cautions)
	}
	if b.Topology.Profile != ProfileSameVendorExt || b.Topology.Version != TopologyVersion {
		t.Fatalf("briefing topology block mismatch: %+v", b.Topology)
	}
	if v, _ := facetValue(b.Topology.Facets, "reviewer_session_mode"); v != SessionModeResumed {
		t.Fatalf("confirmation resume must record session mode resumed, got %s", v)
	}

	// Undeclared topology: undeclared caution, ready-with-cautions at best.
	u, _, _ := newTopologySession(t, "fake", DefaultTopologyPolicy(), []adapter.FakeResult{approve()})
	if _, _, err := u.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	ub, err := BuildBriefing(u.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !briefingHasReason(ub.Cautions, "topology-undeclared") {
		t.Fatalf("undeclared topology must surface a caution: %+v", ub.Cautions)
	}
	if ub.Readiness != BriefingReadyWithCautions {
		t.Fatalf("undeclared topology must not report plain ready, got %s", ub.Readiness)
	}
	// Human rendering shows the facts and their sources.
	human := RenderBriefingHuman(ub)
	for _, needle := range []string{"topology: profile=undeclared", "driver_vendor=undeclared (unknown-undeclared)", "reviewer_vendor=fake (runtime-observed)"} {
		if !strings.Contains(human, needle) {
			t.Fatalf("human briefing missing %q:\n%s", needle, human)
		}
	}
}

// R1-CX-F1 fixture 1: a first dispatch that FAILS without returning a session
// ref still records the reviewer vendor fact; no session mode is invented.
func TestFailedFirstDispatchPreservesReviewerVendor(t *testing.T) {
	s, _, _ := newTopologySession(t, "fake", DefaultTopologyPolicy(),
		[]adapter.FakeResult{{Err: errFakeDispatch}})
	if _, outcome, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil || outcome != review.OutcomeFailed {
		t.Fatalf("failed dispatch must capture outcome=failed: outcome=%s err=%v", outcome, err)
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st.Vendor != "fake" || st.SessionRef != "" || st.ReviewerSessionMode != "" {
		t.Fatalf("vendor fact must survive a no-ref failure without inventing a session: %+v", st)
	}
	if v, src := facetValue(TopologyFacets(st), "reviewer_vendor"); v != "fake" || src != FacetRuntimeObserved {
		t.Fatalf("reviewer vendor must be runtime-observed after a failed dispatch, got %s/%s", v, src)
	}
	if v, _ := facetValue(TopologyFacets(st), "reviewer_session_mode"); v != SessionModeNone {
		t.Fatalf("no session may be projected after a ref-less first failure, got %s", v)
	}
	// Single-reviewer binding (R1 targeted recheck): the recorded vendor binds
	// even without a session ref — another vendor needs an explicit reset.
	other := &adapter.FakeAdapter{VendorName: "codex", NativeHandle: "codex-fixture-1",
		Script: []adapter.FakeResult{approve()}}
	sOther := &Session{Adapter: other, Handles: s.Handles, Canonical: s.Canonical}
	if _, _, err := sOther.Review(context.Background(), "switch", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "bound to fake") {
		t.Fatalf("ref-less recorded vendor must still bind the objective, got: %v", err)
	}
	// The bound vendor itself may continue with a new session.
	same := &adapter.FakeAdapter{VendorName: "fake", NativeHandle: "native-fixture-1",
		Script: []adapter.FakeResult{approve()}}
	sSame := &Session{Adapter: same, Handles: s.Handles, Canonical: s.Canonical}
	if _, _, err := sSame.Review(context.Background(), "continuation", adapter.Request{}); err != nil {
		t.Fatalf("the bound vendor's new-session continuation must be allowed: %v", err)
	}
}

// R1-CX-F1 fixture 2: a resume dispatch that FAILS without returning a ref
// records the resumed fact — the non-fresh-context caution survives.
func TestFailedResumeDispatchKeepsResumedFact(t *testing.T) {
	s, _, _ := newTopologySession(t, "fake", DefaultTopologyPolicy(),
		[]adapter.FakeResult{approve(), {Err: errFakeDispatch}})
	if _, _, err := s.Review(context.Background(), "first", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReviewerSessionMode != SessionModeNew {
		t.Fatalf("first dispatch must record new, got %q", st.ReviewerSessionMode)
	}
	if _, outcome, err := s.Review(context.Background(), "resume fails", adapter.Request{}); err != nil || outcome != review.OutcomeFailed {
		t.Fatalf("resume failure must capture outcome=failed: outcome=%s err=%v", outcome, err)
	}
	st, err = LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st.ReviewerSessionMode != SessionModeResumed {
		t.Fatalf("ref-less resume failure must record resumed, got %q", st.ReviewerSessionMode)
	}
	b, err := BuildBriefing(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !briefingHasReason(b.Cautions, "reviewer-session-resumed") {
		t.Fatalf("resumed caution must survive a failed resume: %+v", b.Cautions)
	}
}

// R1-CX-F1 fixtures 3/4: prepared review/confirmation journals reconciled to
// UNKNOWN preserve the journal reviewer vendor and conservatively downgrade a
// stored session's mode to unknown — never presenting the pre-crash mode as
// current.
func TestPreparedJournalReconcilePreservesVendorAndDowngradesMode(t *testing.T) {
	for _, kind := range []string{"review", "confirmation"} {
		t.Run(kind, func(t *testing.T) {
			s, _, _ := newTopologySession(t, "fake", DefaultTopologyPolicy(), []adapter.FakeResult{approve()})
			if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
				t.Fatal(err)
			}
			round := 0
			if kind == "review" {
				round = 1
			}
			journal := createPreparedJournalForTest(t, s, kind, round)
			if _, err := Reconcile(s.Canonical, journal); err != nil {
				t.Fatal(err)
			}
			st, err := LoadState(s.Canonical)
			if err != nil {
				t.Fatal(err)
			}
			if st.Vendor != "fake" {
				t.Fatalf("reconcile must preserve the journal reviewer vendor: %q", st.Vendor)
			}
			if st.ReviewerSessionMode != SessionModeUnknown {
				t.Fatalf("reconcile must downgrade the stored session mode to unknown, got %q", st.ReviewerSessionMode)
			}
			if v, src := facetValue(TopologyFacets(st), "reviewer_session_mode"); v != SessionModeUnknown || src != FacetUnknownUndeclared {
				t.Fatalf("unknown mode must project unknown-undeclared, got %s/%s", v, src)
			}
			b, err := BuildBriefing(s.Canonical)
			if err != nil {
				t.Fatal(err)
			}
			if !briefingHasReason(b.Cautions, "reviewer-session-resumed") {
				t.Fatalf("non-fresh caution must survive an UNKNOWN reconcile: %+v", b.Cautions)
			}
		})
	}
}

// R1-CX-F1 fixture 3b: a prepared journal reconciled before any session
// exists still records the reviewer vendor without inventing a session mode.
func TestPreparedJournalReconcileWithoutSessionRecordsVendorOnly(t *testing.T) {
	s, _, _ := newTopologySession(t, "fake", DefaultTopologyPolicy(), nil)
	journal := createPreparedJournalForTest(t, s, "review", 0)
	if _, err := Reconcile(s.Canonical, journal); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st.Vendor != "fake" || st.SessionRef != "" || st.ReviewerSessionMode != "" {
		t.Fatalf("vendor-only preservation expected: %+v", st)
	}
}

// R1 confirmation issue A (Option A): a cross-vendor reset whose dispatch
// starts but returns no new session ref abandons the prior binding — the
// prior vendor's ref is never combined with the attempted vendor, and the
// next dispatch starts a new session as the explicit reset continuation.
func TestFailedCrossVendorResetClearsPriorBinding(t *testing.T) {
	s, _, _ := newTopologySession(t, "claude", mustTopology(t, "", "claude", "separate"), []adapter.FakeResult{approve()})
	if _, _, err := s.Review(context.Background(), "first", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	codex := &adapter.FakeAdapter{VendorName: "codex", NativeHandle: "codex-fixture-1",
		Script: []adapter.FakeResult{{Err: errFakeDispatch}, approve()}}
	s2 := &Session{
		Adapter:   codex,
		Handles:   s.Handles,
		Canonical: s.Canonical,
		Reset:     &SessionReset{Mode: "second-opinion", Reason: "cross-vendor reset fixture"},
	}
	if _, outcome, err := s2.Review(context.Background(), "reset fails", adapter.Request{}); err != nil || outcome != review.OutcomeFailed {
		t.Fatalf("failed reset must capture outcome=failed: outcome=%s err=%v", outcome, err)
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st.Vendor != "codex" || st.SessionRef != "" || st.ReviewerSessionMode != "" {
		t.Fatalf("failed cross-vendor reset must clear the prior binding and keep the attempted vendor: %+v",
			map[string]string{"vendor": st.Vendor, "ref": st.SessionRef, "mode": st.ReviewerSessionMode})
	}
	if len(st.SessionChanges) != 1 || st.SessionChanges[0].ToVendor != "codex" {
		t.Fatalf("the reset attempt must stay auditable in session changes: %+v", st.SessionChanges)
	}
	// Single-reviewer binding (R1 targeted recheck): with the ref cleared, the
	// attempted vendor stays bound — a different vendor without an explicit
	// reset fails closed before Prepare and mutates nothing.
	beforeRev, err := store.Revision(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	claudeAgain := &adapter.FakeAdapter{VendorName: "claude", NativeHandle: "01234567-89ab-cdef-0123-456789abcdef",
		Script: []adapter.FakeResult{approve()}}
	sBad := &Session{Adapter: claudeAgain, Handles: s.Handles, Canonical: s.Canonical}
	if _, _, err := sBad.Review(context.Background(), "silent switch", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "bound to codex") {
		t.Fatalf("vendor switch without reset must fail closed on the bound vendor, got: %v", err)
	}
	if claudeAgain.Prepared != 0 {
		t.Fatal("the rejected vendor switch must fail before Prepare")
	}
	afterRev, err := store.Revision(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if beforeRev != afterRev {
		t.Fatal("the rejected vendor switch must not mutate the canonical")
	}
	// Explicit continuation: the next dispatch starts a new codex session —
	// never a silent resume of the abandoned claude ref.
	s3 := &Session{Adapter: codex, Handles: s.Handles, Canonical: s.Canonical}
	if _, _, err := s3.Review(context.Background(), "continuation", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	st, err = LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st.Vendor != "codex" || st.SessionRef == "" || st.ReviewerSessionMode != SessionModeNew {
		t.Fatalf("reset continuation must be a new session of the attempted vendor: %+v",
			map[string]string{"vendor": st.Vendor, "ref": st.SessionRef, "mode": st.ReviewerSessionMode})
	}
}

// R1 confirmation issue A: a prepared journal from a cross-vendor reset
// dispatch reconciled to UNKNOWN follows the same rule — vendor fact from the
// journal, prior binding cleared, never a combined cross-vendor pair.
func TestPreparedCrossVendorResetReconcileClearsPriorBinding(t *testing.T) {
	s, _, _ := newTopologySession(t, "claude", mustTopology(t, "", "claude", "separate"), []adapter.FakeResult{approve()})
	if _, _, err := s.Review(context.Background(), "first", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	journal := createPreparedJournalVendorForTest(t, s, "review", 1, "codex")
	if _, err := Reconcile(s.Canonical, journal); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st.Vendor != "codex" || st.SessionRef != "" || st.ReviewerSessionMode != "" {
		t.Fatalf("cross-vendor reset reconcile must clear the prior binding: %+v",
			map[string]string{"vendor": st.Vendor, "ref": st.SessionRef, "mode": st.ReviewerSessionMode})
	}
	if v, src := facetValue(TopologyFacets(st), "reviewer_vendor"); v != "codex" || src != FacetRuntimeObserved {
		t.Fatalf("journal reviewer vendor must be preserved, got %s/%s", v, src)
	}
}

// R1-CX-F2: execution_surface is operator-declared init policy and its source
// stays identical before and after dispatch.
func TestExecutionSurfaceSourceIsOperatorDeclaredAndStable(t *testing.T) {
	s, _, _ := newTopologySession(t, "fake", DefaultTopologyPolicy(), []adapter.FakeResult{approve()})
	assertSource := func(stage string) {
		st, err := LoadState(s.Canonical)
		if err != nil {
			t.Fatal(err)
		}
		if v, src := facetValue(TopologyFacets(st), "execution_surface"); v != SurfaceExternalCLI || src != FacetOperatorDeclared {
			t.Fatalf("%s: execution_surface must be %s/operator-declared, got %s/%s", stage, SurfaceExternalCLI, v, src)
		}
	}
	assertSource("pre-dispatch")
	if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	assertSource("post-dispatch")
}

// R0-CX-F5: the reviewer prompt asserts no independence; relation facts are
// delivered as separate source-qualified provenance.
func TestReviewerPromptNeutralityAndRelationFacts(t *testing.T) {
	if strings.Contains(adapter.ReviewerTrustSystemPrompt, "independent") {
		t.Fatal("trust prompt must not assert reviewer independence")
	}
	if !strings.Contains(adapter.ReviewerTrustSystemPrompt, "designated reviewer") {
		t.Fatal("trust prompt must use the neutral designated-reviewer wording")
	}
	s, _, _ := newTopologySession(t, "claude", mustTopology(t, "", "claude", "separate"), []adapter.FakeResult{approve()})
	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	prompt := subjectPrompt(st, "review this")
	for _, needle := range []string{
		"Reviewer relation facts",
		"declared values are not verified",
		"driver_vendor: claude (operator-declared)",
		"context_relation: separate (operator-declared)",
	} {
		if !strings.Contains(prompt, needle) {
			t.Fatalf("prompt missing relation fact %q:\n%s", needle, prompt)
		}
	}
}

// Carried sessions from a related objective project the carried session fact
// before any dispatch in the new objective.
func TestCarriedSessionProjectsCarriedMode(t *testing.T) {
	s, _, dir := newTopologySession(t, "claude", mustTopology(t, "", "claude", "separate"), []adapter.FakeResult{approve()})
	if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Close(s.Canonical, "owner", "owner", ""); err != nil {
		t.Fatal(err)
	}
	spec, err := subject.SingleFile(targetPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	st, err := InitSubjectTopology(s.Canonical, "follow-up", spec, "obj-prior", "second pass on the same target", true, approvedPolicy(t), mustTopology(t, "", "claude", "separate"))
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionRef == "" || st.ReviewerSessionMode != "" {
		t.Fatalf("carried session must keep the ref with no dispatch-observed mode: %+v", st)
	}
	if v, _ := facetValue(TopologyFacets(st), "reviewer_session_mode"); v != SessionModeCarried {
		t.Fatalf("carried session must project carried, got %s", v)
	}
	b, err := BuildBriefing(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !briefingHasReason(b.Cautions, "reviewer-session-resumed") {
		t.Fatalf("carried session must surface the non-fresh-context caution: %+v", b.Cautions)
	}
}
