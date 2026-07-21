package relay

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/store"
)

func briefingHasReason(reasons []BriefingReason, code string) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func confirmationResult(id, status string) adapter.FakeResult {
	return adapter.FakeResult{Structured: map[string]any{
		"results": []any{map[string]any{
			"id": id, "status": status,
			"examined": []any{evidence("target.go", `func greet() string { return "hello" }`)},
		}},
	}}
}

func TestBriefingReadyIsPureVersionedAndPrivate(t *testing.T) {
	s, _, dir := newSession(t, []adapter.FakeResult{approve()})
	if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Revision(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	b1, err := BuildBriefing(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := BuildBriefing(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.Revision(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if before != after || b1.CanonicalRevision != before {
		t.Fatalf("briefing mutated or misreported canonical: before=%s after=%s briefing=%s", before, after, b1.CanonicalRevision)
	}
	if b1.Version != BriefingOutputVersion || b1.Readiness != BriefingReady || b1.FinalReviewedRound == nil {
		t.Fatalf("ready briefing contract mismatch: %+v", b1)
	}
	j1, _ := MarshalBriefingJSON(b1)
	j2, _ := MarshalBriefingJSON(b2)
	if string(j1) != string(j2) {
		t.Fatal("repeated briefing render must be byte-stable for unchanged canonical state")
	}
	human := RenderBriefingHuman(b1)
	for _, forbidden := range []string{
		dir, `"session_ref"`, `"resolved_root"`, `"resolved_path"`, `"excerpt"`,
		`"verbatim"`, `"durable_anchor":`, "native-1",
	} {
		if strings.Contains(string(j1), forbidden) || strings.Contains(human, forbidden) {
			t.Fatalf("briefing leaked excluded private field %q\nJSON:\n%s\nHUMAN:\n%s", forbidden, j1, human)
		}
	}
	if len(b1.Evidence) != 1 || b1.Evidence[0].Claim == "" || b1.AssuranceCounts.ContentMatch != 1 {
		t.Fatalf("no-finding approve must show examined evidence and assurance: %+v", b1.Evidence)
	}
	if _, err := Close(s.Canonical, "owner", "owner", ""); err != nil {
		t.Fatalf("ready briefing must agree with actual Close: %v", err)
	}
}

func TestBriefingNotConfirmedIsCautionAndCloseStillSucceeds(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{
		changesRequested("finding one"), confirmationResult("R0-F1", "not-confirmed"),
	})
	st, _, err := s.Review(context.Background(), "review", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Disposition(s.Canonical, "R0-F1", acceptDisposition()); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfirmation(s.Canonical, 0, []string{"R0-F1"}); err != nil {
		t.Fatal(err)
	}
	if _, done, err := s.ConfirmWithReviewer(context.Background(), 0, st.TargetRevision,
		[]string{"R0-F1"}, "driver-claimed delta is intentionally not projected", adapter.Request{}); err != nil || done {
		t.Fatalf("not-confirmed fixture failed: done=%v err=%v", done, err)
	}
	b, err := BuildBriefing(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if b.Readiness != BriefingReadyWithCautions || len(b.BlockingReasons) != 0 ||
		!briefingHasReason(b.Cautions, "confirmation-not-confirmed") {
		t.Fatalf("not-confirmed must be advisory only: readiness=%s blockers=%+v cautions=%+v",
			b.Readiness, b.BlockingReasons, b.Cautions)
	}
	if len(b.Findings) != 1 || b.Findings[0].Confirmation != "not-confirmed" {
		t.Fatalf("finding confirmation projection missing: %+v", b.Findings)
	}
	encoded, _ := MarshalBriefingJSON(b)
	if strings.Contains(string(encoded), "driver-claimed delta") {
		t.Fatal("unverified confirmation claimed_delta prose must not enter the briefing DTO")
	}
	if _, err := Close(s.Canonical, "owner", "owner", ""); err != nil {
		t.Fatalf("advisory confirmation must not silently become a Close blocker: %v", err)
	}
}

func TestBriefingBlockedParityForPendingAndStaleTarget(t *testing.T) {
	t.Run("pending transaction", func(t *testing.T) {
		s, _, dir := newSession(t, []adapter.FakeResult{approve()})
		if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
			t.Fatal(err)
		}
		journal := s.Canonical + ".dispatch-tx-" + strings.Repeat("0", 32) + ".json"
		if err := os.WriteFile(journal, []byte("private pending payload"), 0o600); err != nil {
			t.Fatal(err)
		}
		b, err := BuildBriefing(s.Canonical)
		if err != nil {
			t.Fatal(err)
		}
		if b.Readiness != BriefingBlocked || !briefingHasReason(b.BlockingReasons, "pending-transaction") {
			t.Fatalf("pending transaction not classified as blocked: %+v", b.BlockingReasons)
		}
		encoded, _ := MarshalBriefingJSON(b)
		if strings.Contains(string(encoded), dir) || strings.Contains(string(encoded), journal) {
			t.Fatalf("pending classification leaked private path: %s", encoded)
		}
		if _, err := Close(s.Canonical, "owner", "owner", ""); err == nil || !strings.Contains(err.Error(), "pending transactions") {
			t.Fatalf("blocked pending briefing must agree with Close: %v", err)
		}
	})

	t.Run("stale target", func(t *testing.T) {
		s, _, dir := newSession(t, []adapter.FakeResult{approve()})
		if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(targetPath(dir), []byte("changed after review"), 0o600); err != nil {
			t.Fatal(err)
		}
		b, err := BuildBriefing(s.Canonical)
		if err != nil {
			t.Fatal(err)
		}
		if b.Readiness != BriefingBlocked || !briefingHasReason(b.BlockingReasons, "stale-target") {
			t.Fatalf("stale target not classified as blocked: %+v", b.BlockingReasons)
		}
		encoded, _ := MarshalBriefingJSON(b)
		if strings.Contains(string(encoded), dir) {
			t.Fatalf("stale-target reason leaked absolute root: %s", encoded)
		}
		if _, err := Close(s.Canonical, "owner", "owner", ""); err == nil {
			t.Fatal("blocked stale briefing must agree with Close failure")
		}
	})
}

func TestBriefingExecutionReasonsRoundBoundAndTerminal(t *testing.T) {
	for _, tt := range []struct {
		name string
		fake adapter.FakeResult
		code string
	}{
		{name: "failed", fake: adapter.FakeResult{Err: os.ErrPermission}, code: "execution-failed"},
		{name: "unknown", fake: adapter.FakeResult{TimedOut: true, TimeoutKind: adapter.TimeoutHardCap}, code: "execution-unknown"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _, _ := newSession(t, []adapter.FakeResult{tt.fake})
			s.FormalRoundBound = 1
			if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
				t.Fatal(err)
			}
			b, err := BuildBriefing(s.Canonical)
			if err != nil {
				t.Fatal(err)
			}
			if b.Readiness != BriefingBlocked || !briefingHasReason(b.BlockingReasons, tt.code) ||
				!briefingHasReason(b.BlockingReasons, "round-bound-exhausted") {
				t.Fatalf("execution/bound classification mismatch: %+v", b.BlockingReasons)
			}
			if _, err := Close(s.Canonical, "owner", "owner", ""); err == nil {
				t.Fatal("blocked execution briefing must agree with Close failure")
			}
		})
	}

	for _, bound := range []int{1, 3, 5} {
		t.Run("ready-bound-"+string(rune('0'+bound)), func(t *testing.T) {
			s, _, _ := newSession(t, []adapter.FakeResult{approve()})
			s.FormalRoundBound = bound
			if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
				t.Fatal(err)
			}
			b, err := BuildBriefing(s.Canonical)
			if err != nil || b.Readiness != BriefingReady || b.Objective.FormalRoundBound != bound {
				t.Fatalf("bound %d briefing mismatch: readiness=%s err=%v", bound, b.Readiness, err)
			}
		})
	}

	s, _, _ := newSession(t, []adapter.FakeResult{approve()})
	if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Close(s.Canonical, "owner", "owner", ""); err != nil {
		t.Fatal(err)
	}
	b, err := BuildBriefing(s.Canonical)
	if err != nil || b.Readiness != BriefingTerminal || BriefingCheckExitCode(b.Readiness) != BriefingCheckTerminal {
		t.Fatalf("terminal briefing mismatch: %+v err=%v", b, err)
	}
}

func TestBriefingContradictionTransitionsFromBlockerToCaution(t *testing.T) {
	result := approve()
	result.Structured["findings"] = []any{map[string]any{
		"summary": "contradictory blocking evidence", "reviewer_severity": "high",
		"evidence": []any{"E1"}, "recommendation": "inspect before close",
	}}
	s, _, _ := newSession(t, []adapter.FakeResult{result})
	if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	blocked, err := BuildBriefing(s.Canonical)
	if err != nil || !briefingHasReason(blocked.BlockingReasons, "verdict-contradiction") {
		t.Fatalf("current contradiction must block: %+v err=%v", blocked, err)
	}
	if _, err := Disposition(s.Canonical, "R0-F1", acceptDisposition()); err != nil {
		t.Fatal(err)
	}
	ready, err := BuildBriefing(s.Canonical)
	if err != nil || ready.Readiness != BriefingReadyWithCautions ||
		!briefingHasReason(ready.Cautions, "verdict-contradiction-history") {
		t.Fatalf("resolved contradiction must remain visible as caution: %+v err=%v", ready, err)
	}
	if _, err := Close(s.Canonical, "owner", "owner", ""); err != nil {
		t.Fatalf("resolved contradiction caution must not block Close: %v", err)
	}
}

func TestBriefingCheckExitCodesAreStable(t *testing.T) {
	checks := map[BriefingReadiness]int{
		BriefingReady:             BriefingCheckReady,
		BriefingReadyWithCautions: BriefingCheckReadyWithCautions,
		BriefingBlocked:           BriefingCheckBlocked,
		BriefingTerminal:          BriefingCheckTerminal,
	}
	for readiness, want := range checks {
		if got := BriefingCheckExitCode(readiness); got != want {
			t.Fatalf("%s exit code = %d, want %d", readiness, got, want)
		}
	}
	if BriefingCheckExitCode(BriefingReadiness("future")) != BriefingCheckBlocked {
		t.Fatal("unknown readiness must fail closed to the blocked check code")
	}
}

func TestBriefingApprovalResponseShowsCompletenessWithoutRawText(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{approve()})
	if _, _, err := s.Review(context.Background(), "review", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	requestInput := review.ApprovalRequestInput{
		Type: "acrelay.test.owner-choice", Scope: "current objective only", Reason: "owner choice required",
		Options: []review.ApprovalOption{{ID: "accept", Description: "accept current result"}},
	}
	_, request, err := RequestApproval(s.Canonical, requestInput, "driver", "driver-a")
	if err != nil {
		t.Fatal(err)
	}
	secret := "/private/owner/response/path"
	if _, resolved, err := RespondApproval(s.Canonical, request.ID, review.OwnerResponse{
		Actor: "owner", Verbatim: "approved with " + secret, RespondedAt: "2026-07-21",
		Decision: "accept", DecisionScope: requestInput.Scope,
		DurableAnchor: secret, Unambiguous: true,
	}); err != nil || !resolved {
		t.Fatalf("approval response fixture failed: resolved=%v err=%v", resolved, err)
	}
	b, err := BuildBriefing(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := MarshalBriefingJSON(b)
	if strings.Contains(string(encoded), secret) || len(b.ApprovalRequests) != 1 ||
		b.ApprovalRequests[0].Response == nil || !b.ApprovalRequests[0].Response.VerbatimRecorded ||
		!b.ApprovalRequests[0].Response.DurableAnchorRecorded {
		t.Fatalf("approval projection leaked raw text or lost completeness: %s", encoded)
	}
}
