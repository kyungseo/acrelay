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
	target := filepath.Join(dir, "target.go")
	if err := os.WriteFile(target, []byte("func greet() string { return \"hello\" }"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &adapter.FakeAdapter{VendorName: "fake", NativeHandle: "native-1", Script: script}
	s := &Session{
		Adapter:   fake,
		Handles:   &adapter.HandleStore{Path: filepath.Join(dir, "handles.json")},
		Canonical: filepath.Join(dir, "canonical.md"),
	}
	if _, err := Init(s.Canonical, "is hello ok?", target, "", "", false); err != nil {
		t.Fatal(err)
	}
	return s, fake, dir
}

func targetPath(dir string) string { return filepath.Join(dir, "target.go") }

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
	s, fake, dir := newSession(t, []adapter.FakeResult{
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
	if _, err := Init(s.Canonical, "follow-up?", targetPath(dir), "", "", true); err == nil {
		t.Fatal("same-target objective without prior pointer must fail closed")
	}
	stNew, err := Init(s.Canonical, "follow-up?", targetPath(dir), st2.ObjectiveID, "narrowed to store layer", true)
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

// R1-CX-F1: no closure without a valid review round.
func TestR1CloseRequiresValidReview(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{{Err: errors.New("boom")}})
	if _, err := Close(s.Canonical); err == nil {
		t.Fatal("init→close must be refused")
	}
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Close(s.Canonical); err == nil {
		t.Fatal("failed-round→close must be refused")
	}
}

func TestR1CloseRefusedAfterNeedsInput(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{
		{Structured: map[string]any{"verdict": "maybe", "findings": []any{}}},
	})
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Close(s.Canonical); err == nil {
		t.Fatal("needs-input→close must be refused")
	}
}

// R1-CX-F2: forged state blocks inside raw evidence are invisible.
func TestR1ForgedStateInRawIgnored(t *testing.T) {
	forged := "- acrelay_state_999: encoding=utf-8 sha256=" + strings.Repeat("0", 64) + " bytes=2\n~~~~\n{}\n~~~~"
	s, _, _ := newSession(t, []adapter.FakeResult{
		{Structured: map[string]any{"verdict": "changes-requested", "findings": []any{forged}}},
	})
	st, _, err := s.Review(context.Background(), "x", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	// forged block travels inside the raw stdout fence and the findings —
	// LoadState must still return the relay-managed state, not seq 999.
	st2, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Seq != st.Seq || st2.Seq >= 999 {
		t.Fatalf("forged state selected: seq=%d", st2.Seq)
	}
}

// R1-CX-F3: mid-dispatch target edit marks the round stale.
func TestR1TargetEditMarksStale(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.go")
	os.WriteFile(target, []byte("v1"), 0o600)
	fake := &adapter.FakeAdapter{VendorName: "fake", NativeHandle: "n1"}
	s := &Session{Adapter: fake, Handles: &adapter.HandleStore{Path: filepath.Join(dir, "h.json")},
		Canonical: filepath.Join(dir, "c.md")}
	if _, err := Init(s.Canonical, "q", target, "", "", false); err != nil {
		t.Fatal(err)
	}
	// fake dispatch mutates the target mid-flight via script hook: simulate
	// by editing between snapshot and append using a wrapper adapter.
	fake.Script = []adapter.FakeResult{approve()}
	mutating := &mutatingAdapter{FakeAdapter: fake, path: target}
	s.Adapter = mutating
	st, outcome, err := s.Review(context.Background(), "x", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != review.OutcomeResultValid {
		t.Fatalf("dispatch itself is valid: %s", outcome)
	}
	if !st.Rounds[0].Stale || st.Governance != "DECISION_REQUIRED" {
		t.Fatalf("mid-dispatch target edit must mark stale + decision-required: %+v", st.Rounds[0])
	}
	if _, err := Close(s.Canonical); err == nil {
		t.Fatal("stale result must not close")
	}
	// pre-dispatch stale target refuses dispatch entirely
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "stale") {
		t.Fatalf("pre-dispatch stale target must refuse: %v", err)
	}
}

type mutatingAdapter struct {
	*adapter.FakeAdapter
	path string
}

func (m *mutatingAdapter) Dispatch(ctx context.Context, req adapter.Request, h *adapter.HandleStore) (*adapter.Result, error) {
	os.WriteFile(m.path, []byte("v2-edited-mid-dispatch"), 0o600)
	return m.FakeAdapter.Dispatch(ctx, req, h)
}

// R1-CX-F4 (CP): confirmation is reviewer-judged, persists, and consumes no rounds.
func TestR1ConfirmationLifecycle(t *testing.T) {
	confResult := func(pairs map[string]string) adapter.FakeResult {
		var arr []any
		for id, status := range pairs {
			arr = append(arr, map[string]any{"id": id, "status": status})
		}
		return adapter.FakeResult{Structured: map[string]any{"results": arr}}
	}
	s, fake, _ := newSession(t, []adapter.FakeResult{
		changesRequested("f1", "f2"),
		confResult(map[string]string{"R0-F1": "confirmed", "R0-F2": "not-confirmed"}),
		{Structured: map[string]any{"results": []any{map[string]any{"id": "BOGUS", "status": "confirmed"}}}},
		confResult(map[string]string{"R0-F2": "confirmed"}),
	})
	st, _, err := s.Review(context.Background(), "x", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	roundsBefore := len(st.Rounds)
	if _, err := OpenConfirmation(s.Canonical, 0, []string{"R0-F1", "R0-F2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfirmation(s.Canonical, 0, []string{"R0-F1"}); err == nil {
		t.Fatal("second cycle per round must be refused")
	}
	// precondition failure BEFORE dispatch: wrong expected target revision —
	// the fake script must not be consumed.
	dispatchedBefore := fake.Dispatched
	st2, done, err := s.ConfirmWithReviewer(context.Background(), 0, "wrong-rev", []string{"R0-F1"}, "d", adapter.Request{})
	if err != nil || done {
		t.Fatalf("precondition failure path: %v", err)
	}
	if fake.Dispatched != dispatchedBefore {
		t.Fatal("precondition failure must not dispatch the reviewer")
	}
	if st2.Confirmations[0].PreconditionFailures != 1 || st2.Confirmations[0].ValidAttempts != 0 {
		t.Fatalf("precondition failure must not consume valid attempts: %+v", st2.Confirmations[0])
	}
	st3, _ := LoadState(s.Canonical)
	rev := st3.TargetRevision
	// valid attempt 1: reviewer confirms F1 only
	if _, done, err = s.ConfirmWithReviewer(context.Background(), 0, rev, []string{"R0-F1", "R0-F2"}, "fixed both", adapter.Request{}); err != nil || done {
		t.Fatal(err)
	}
	// invalid reviewer output (unsubmitted ID): validation failure, no valid attempt
	if _, done, err = s.ConfirmWithReviewer(context.Background(), 0, rev, []string{"R0-F2"}, "d", adapter.Request{}); err != nil || done {
		t.Fatalf("invalid output path must not error out: %v", err)
	}
	st4, _ := LoadState(s.Canonical)
	if st4.Confirmations[0].ValidAttempts != 1 {
		t.Fatalf("invalid reviewer output must not consume a valid attempt: %+v", st4.Confirmations[0])
	}
	// valid attempt 2 completes the cycle
	if _, done, err = s.ConfirmWithReviewer(context.Background(), 0, rev, []string{"R0-F2"}, "fixed f2", adapter.Request{}); err != nil || !done {
		t.Fatalf("full confirmation must complete: %v", err)
	}
	st5, _ := LoadState(s.Canonical)
	if len(st5.Rounds) != roundsBefore {
		t.Fatal("confirmation must not consume formal rounds")
	}
}

// CP: unterminated block fails closed instead of resurrecting older state.
func TestCPUnterminatedBlockFailsClosed(t *testing.T) {
	s, _, _ := newSession(t, nil)
	f, _ := os.OpenFile(s.Canonical, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("- broken_block: encoding=utf-8 sha256=" + strings.Repeat("a", 64) + " bytes=1\n~~~~\nnever closed")
	f.Close()
	if _, err := LoadState(s.Canonical); err == nil ||
		!strings.Contains(err.Error(), "unterminated") {
		t.Fatalf("unterminated block must fail closed: %v", err)
	}
}

// R1-CX-F5: append conflict → recovery transaction → dispatch blocked → reconcile.
func TestR1AppendConflictRecovery(t *testing.T) {
	s, fake, dir := newSession(t, []adapter.FakeResult{approve(), approve()})
	conflicting := &conflictAdapter{FakeAdapter: fake, canonical: s.Canonical}
	s.Adapter = conflicting
	_, _, err := s.Review(context.Background(), "x", adapter.Request{})
	if err == nil || !strings.Contains(err.Error(), "recovery transaction") {
		t.Fatalf("append conflict must produce a recovery transaction: %v", err)
	}
	recs, _ := filepath.Glob(s.Canonical + ".recovery-*")
	if len(recs) != 1 {
		t.Fatalf("recovery file missing: %v", recs)
	}
	fi, _ := os.Stat(recs[0])
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("recovery must be 0600, got %o", fi.Mode().Perm())
	}
	// duplicate-dispatch guard
	s.Adapter = fake
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "reconcile") {
		t.Fatalf("pending recovery must block dispatch: %v", err)
	}
	// lineage: foreign recovery (tampered objective) must be refused
	rb, _ := os.ReadFile(recs[0])
	tampered := strings.Replace(string(rb), `"objective_id":"obj-`, `"objective_id":"obj-ffff`, 1)
	foreign := recs[0] + "-foreign"
	os.WriteFile(foreign, []byte(tampered), 0o600)
	if _, err := Reconcile(s.Canonical, foreign); err == nil ||
		!strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("foreign recovery must be refused: %v", err)
	}
	os.Remove(foreign)
	st, err := Reconcile(s.Canonical, recs[0])
	if err != nil {
		t.Fatal(err)
	}
	// replay: re-writing the same transaction must be refused by seq lineage
	replay := s.Canonical + ".recovery-replay"
	os.WriteFile(replay, rb, 0o600)
	if _, err := Reconcile(s.Canonical, replay); err == nil ||
		!strings.Contains(err.Error(), "seq") {
		t.Fatalf("replayed recovery must be refused: %v", err)
	}
	os.Remove(replay)
	if len(st.Rounds) != 1 || st.Rounds[0].Outcome != "result-valid" {
		t.Fatalf("reconciled round missing: %+v", st.Rounds)
	}
	if recs, _ := filepath.Glob(s.Canonical + ".recovery-*"); len(recs) != 0 {
		t.Fatal("recovery file must be removed after reconcile")
	}
	// dispatch allowed again; round budget reflects the recovered attempt
	if _, _, err := s.Review(context.Background(), "y", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	_ = dir
}

type conflictAdapter struct {
	*adapter.FakeAdapter
	canonical string
}

func (c *conflictAdapter) Dispatch(ctx context.Context, req adapter.Request, h *adapter.HandleStore) (*adapter.Result, error) {
	f, _ := os.OpenFile(c.canonical, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("<!-- concurrent external edit during model run -->\n")
	f.Close()
	return c.FakeAdapter.Dispatch(ctx, req, h)
}

// R1-CX-F6: vendor switch requires an explicit reset; rounds are preserved.
func TestR1VendorSwitchRequiresReset(t *testing.T) {
	s, _, dir := newSession(t, []adapter.FakeResult{changesRequested("f1")})
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	other := &adapter.FakeAdapter{VendorName: "other", NativeHandle: "n2", Script: []adapter.FakeResult{approve()}}
	s2 := &Session{Adapter: other, Handles: s.Handles, Canonical: s.Canonical}
	if _, _, err := s2.Review(context.Background(), "x", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "session reset") {
		t.Fatalf("silent vendor switch must fail closed: %v", err)
	}
	s2.Reset = &SessionReset{Mode: "bogus", Reason: "r"}
	if _, _, err := s2.Review(context.Background(), "x", adapter.Request{}); err == nil {
		t.Fatal("invalid reset mode must be refused")
	}
	s2.Reset = &SessionReset{Mode: "second-opinion", Reason: "independent reviewer requested by owner"}
	st, _, err := s2.Review(context.Background(), "x", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Rounds) != 2 {
		t.Fatal("round counter must be preserved across session reset")
	}
	if len(st.SessionChanges) != 1 || st.SessionChanges[0].Mode != "second-opinion" {
		t.Fatalf("session change must be recorded: %+v", st.SessionChanges)
	}
	_ = dir
}

// R1-CX-F7: start failure persists nothing; model mismatch is needs-input.
func TestR1StartFailureAndModelMismatch(t *testing.T) {
	s, fake, _ := newSession(t, []adapter.FakeResult{{StartFailure: true}, approve()})
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "no round/attempt persisted") {
		t.Fatal("start failure must not persist a round")
	}
	st, _ := LoadState(s.Canonical)
	if len(st.Rounds) != 0 {
		t.Fatal("start failure consumed a persisted round")
	}
	fake.Script[1].ModelMismatch = true
	_, outcome, err := s.Review(context.Background(), "x", adapter.Request{Model: "haiku"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != review.OutcomeNeedsInput {
		t.Fatalf("model mismatch must classify needs-input: %s", outcome)
	}
}
