package relay

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/review"
)

func newSession(t *testing.T, script []adapter.FakeResult) (*Session, *adapter.FakeAdapter, string) {
	t.Helper()
	dir := t.TempDir()
	fake := &adapter.FakeAdapter{VendorName: "fake", NativeHandle: "native-1", Script: script}
	s := &Session{
		Adapter:   fake,
		Handles:   &adapter.HandleStore{Path: filepath.Join(dir, "handles.json")},
		Canonical: filepath.Join(dir, "canonical.md"),
	}
	if _, err := Init(s.Canonical, "is hello ok?", "rev-1", "", "", false); err != nil {
		t.Fatal(err)
	}
	return s, fake, dir
}

func approve() adapter.FakeResult {
	return adapter.FakeResult{Structured: map[string]any{"verdict": "approve", "findings": []any{}}}
}

func changesRequested(findings ...any) adapter.FakeResult {
	return adapter.FakeResult{Structured: map[string]any{"verdict": "changes-requested", "findings": findings}}
}

// E2E happy path: init → review(approve) → close.
func TestE2EApproveAndClose(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{approve()})
	st, outcome, err := s.Review(context.Background(), "review hello", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != review.OutcomeResultValid || st.Governance != string(kernel.GovClosable) {
		t.Fatalf("approve must yield result-valid + CLOSABLE: %s %s", outcome, st.Governance)
	}
	if st.SessionRef == "" {
		t.Fatal("session_ref must be recorded")
	}
	st2, err := Close(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Governance != string(kernel.GovClosed) {
		t.Fatalf("got %s", st2.Governance)
	}
}

// E2E: changes-requested blocks closure until every finding is dispositioned.
func TestE2EChangesRequestedClosureGate(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{changesRequested("finding one", "finding two")})
	st, outcome, err := s.Review(context.Background(), "review hello", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != review.OutcomeResultValid || st.Governance != string(kernel.GovDecisionRequired) {
		t.Fatalf("changes-requested must yield DECISION_REQUIRED: %s %s", outcome, st.Governance)
	}
	if _, err := Close(s.Canonical); err == nil {
		t.Fatal("close with undispositioned blocking findings must fail")
	}
	if _, err := Disposition(s.Canonical, "R0-F1", review.DispositionAccept, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Close(s.Canonical); err == nil {
		t.Fatal("close with one remaining blocking finding must fail")
	}
	// needs-user without decision is rejected by closure, with decision passes
	if _, err := Disposition(s.Canonical, "R0-F2", review.DispositionNeedsUser, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Close(s.Canonical); err == nil {
		t.Fatal("needs-user without arbiter decision must block closure")
	}
	if _, err := Disposition(s.Canonical, "R0-F2", review.DispositionNeedsUser,
		&review.ArbiterDecision{Arbiter: "owner", Reason: "accepted for alpha"}); err != nil {
		t.Fatal(err)
	}
	if st3, err := Close(s.Canonical); err != nil || st3.Governance != string(kernel.GovClosed) {
		t.Fatalf("close after full disposition must succeed: %v", err)
	}
}

// R2 bound: a fourth review round is refused.
func TestE2ERoundBound(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{
		changesRequested("f1"), changesRequested("f2"), changesRequested("f3"),
	})
	for i := 0; i < kernel.MaxRoundsPerObjective; i++ {
		if _, _, err := s.Review(context.Background(), "again", adapter.Request{}); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	if _, _, err := s.Review(context.Background(), "again", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "owner decision gate") {
		t.Fatalf("R3 must be refused toward owner gate: %v", err)
	}
}

// F3 wiring: pre-dispatch failure consumes no round.
func TestE2EPreDispatchFailureConsumesNothing(t *testing.T) {
	s, fake, _ := newSession(t, []adapter.FakeResult{approve()})
	fake.PreDispatchFail = errors.New("cli version drift")
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err == nil {
		t.Fatal("pre-dispatch failure must surface")
	}
	st, _ := LoadState(s.Canonical)
	if len(st.Rounds) != 0 {
		t.Fatal("pre-dispatch failure must not consume a round")
	}
	fake.PreDispatchFail = nil
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	st, _ = LoadState(s.Canonical)
	if len(st.Rounds) != 1 {
		t.Fatal("recovered dispatch must consume exactly one round")
	}
}

// Session continuity: round 2 and a follow-up objective reuse the ref.
func TestE2ESessionContinuityAcrossRoundsAndObjectives(t *testing.T) {
	s, fake, _ := newSession(t, []adapter.FakeResult{
		changesRequested("f1"), approve(), approve(),
	})
	st1, _, err := s.Review(context.Background(), "r0", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Disposition(s.Canonical, "R0-F1", review.DispositionAccept, nil); err != nil {
		t.Fatal(err)
	}
	st2, _, err := s.Review(context.Background(), "r1", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if st1.SessionRef == "" || st1.SessionRef != st2.SessionRef {
		t.Fatalf("rounds must reuse the session_ref: %s vs %s", st1.SessionRef, st2.SessionRef)
	}
	if _, err := Close(s.Canonical); err != nil {
		t.Fatal(err)
	}
	// objective transition (DR-811 이관 fixture): same target needs prior
	// pointer, and the reviewer session carries over.
	if _, err := Init(s.Canonical, "follow-up?", "rev-1", "", "", true); err == nil {
		t.Fatal("same-target objective without prior pointer must fail closed")
	}
	stNew, err := Init(s.Canonical, "follow-up?", "rev-1", st2.ObjectiveID, "narrowed to store layer", true)
	if err != nil {
		t.Fatal(err)
	}
	if stNew.CollaborationID != st2.CollaborationID {
		t.Fatal("follow-up objective must stay in the same collaboration")
	}
	if stNew.SessionRef != st2.SessionRef {
		t.Fatal("follow-up objective must carry the reviewer session_ref")
	}
	st3, _, err := s.Review(context.Background(), "r0 of obj2", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if st3.SessionRef != st2.SessionRef {
		t.Fatal("resumed dispatch must keep the same session_ref")
	}
	if fake.Dispatched != 3 {
		t.Fatalf("expected 3 dispatches, got %d", fake.Dispatched)
	}
}

// Dispatch failure marks the round FAILED and the objective needs a decision.
func TestE2EDispatchFailure(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{{Err: errors.New("turn failed")}})
	st, outcome, err := s.Review(context.Background(), "x", adapter.Request{})
	if err != nil {
		t.Fatal(err) // dispatch failure is recorded, not fatal to the relay
	}
	if outcome != review.OutcomeFailed || st.Rounds[0].Attempts[0] != string(kernel.ExecFailed) {
		t.Fatalf("failed dispatch must record FAILED: %s %+v", outcome, st.Rounds[0])
	}
	if st.Governance != string(kernel.GovDecisionRequired) {
		t.Fatal("failed round leaves the objective decision-required")
	}
}

// Timeout marks the attempt UNKNOWN; the next review opens a new round but
// kernel forbids a second attempt in the same round.
func TestE2ETimeoutUnknown(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{{TimedOut: true}})
	st, outcome, err := s.Review(context.Background(), "x", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != review.OutcomeFailed || st.Rounds[0].Attempts[0] != string(kernel.ExecUnknown) {
		t.Fatalf("timeout must record UNKNOWN: %+v", st.Rounds[0])
	}
}

// needs-input: schema-valid dispatch with invalid semantic content.
func TestE2ENeedsInput(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{
		{Structured: map[string]any{"verdict": "maybe", "findings": []any{}}},
	})
	st, outcome, err := s.Review(context.Background(), "x", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != review.OutcomeNeedsInput {
		t.Fatalf("invalid verdict must classify needs-input: %s", outcome)
	}
	if len(st.Findings) != 0 {
		t.Fatal("needs-input must not mint findings")
	}
}

// Terminate: abandoned objective records arbiter+reason and refuses reviews.
func TestE2ETerminate(t *testing.T) {
	s, _, _ := newSession(t, nil)
	if _, err := Terminate(s.Canonical, kernel.GovAbandoned, "", ""); err == nil {
		t.Fatal("terminate without arbiter/reason must fail closed")
	}
	st, err := Terminate(s.Canonical, kernel.GovAbandoned, "owner", "scope cut")
	if err != nil {
		t.Fatal(err)
	}
	if st.Governance != string(kernel.GovAbandoned) {
		t.Fatalf("got %s", st.Governance)
	}
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err == nil {
		t.Fatal("terminal objective must refuse reviews")
	}
}

// Raw evidence round-trips from the canonical document.
func TestE2ERawEvidencePersisted(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{approve()})
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	doc, _ := os.ReadFile(s.Canonical)
	if !strings.Contains(string(doc), "raw_stdout r0a0") {
		t.Fatal("raw stdout block missing from canonical")
	}
	// state history: seq 1 (init) and seq 2 (round) both present
	if !strings.Contains(string(doc), "acrelay_state_1") || !strings.Contains(string(doc), "acrelay_state_2") {
		t.Fatal("state history blocks missing")
	}
}
