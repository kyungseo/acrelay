package relay

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/store"
)

func TestDR813CrashHelper(t *testing.T) {
	mode := os.Getenv("ACRELAY_CRASH_MODE")
	if mode == "" {
		return
	}
	canonical := os.Getenv("ACRELAY_CRASH_CANONICAL")
	target := os.Getenv("ACRELAY_CRASH_TARGET")
	s := &Session{
		Adapter:   &adapter.FakeAdapter{VendorName: "fake", NativeHandle: "native", Script: []adapter.FakeResult{approve()}},
		Handles:   &adapter.HandleStore{Path: filepath.Join(filepath.Dir(canonical), "handles-"+mode+".json")},
		Canonical: canonical,
	}
	switch mode {
	case "bound-only":
		afterFormalRoundBoundAppend = func() { os.Exit(90) }
	case "prepared":
		createPreparedJournalForTest(t, s, "review", 0)
		os.Exit(91)
	case "captured":
		beforeCanonicalAppend = func(string) { os.Exit(92) }
	case "append-cleanup":
		beforeJournalCleanup = func(string) error { os.Exit(93); return nil }
	default:
		t.Fatalf("unknown crash helper mode %q", mode)
	}
	if _, _, err := s.Review(context.Background(), "review "+target, adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash hook did not terminate the helper process")
}

func TestFormalRoundBoundBindOnlyCrash(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	canonical := filepath.Join(dir, "canonical.md")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(canonical, "q", target, "", "", false); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDR813CrashHelper$")
	cmd.Env = append(os.Environ(),
		"ACRELAY_CRASH_MODE=bound-only",
		"ACRELAY_CRASH_CANONICAL="+canonical,
		"ACRELAY_CRASH_TARGET="+target,
	)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("helper did not crash after bound append: %s", out)
	}
	if pending, _ := pendingTransactions(canonical); len(pending) != 0 {
		t.Fatalf("bind-only crash must precede journal creation: %v", pending)
	}
	st, err := LoadState(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if st.FormalRoundBound != kernel.DefaultFormalRoundBound || len(st.Rounds) != 0 || len(st.Transactions) != 0 {
		t.Fatalf("bind-only crash must leave a valid immutable policy without execution: %+v", st)
	}
	fake := &adapter.FakeAdapter{VendorName: "fake", NativeHandle: "native", Script: []adapter.FakeResult{approve()}}
	s := &Session{
		Adapter: fake, Handles: &adapter.HandleStore{Path: filepath.Join(dir, "retry-handles.json")},
		Canonical: canonical, FormalRoundBound: kernel.MaxFormalRoundBound,
	}
	if _, _, err := s.Review(context.Background(), "mismatch", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "immutable") {
		t.Fatalf("bind-only mismatch must fail closed: %v", err)
	}
	if fake.Prepared != 0 || fake.Dispatched != 0 {
		t.Fatal("bind-only mismatch must fail before adapter preparation")
	}
	s.FormalRoundBound = 0
	if _, _, err := s.Review(context.Background(), "same by omission", adapter.Request{}); err != nil {
		t.Fatalf("omitted retry must use the stored bound: %v", err)
	}
}

func TestDR813HostProcessCrashWindows(t *testing.T) {
	for _, mode := range []string{"prepared", "captured", "append-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "target.txt")
			canonical := filepath.Join(dir, "canonical.md")
			if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Init(canonical, "q", target, "", "", false); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestDR813CrashHelper$")
			cmd.Env = append(os.Environ(),
				"ACRELAY_CRASH_MODE="+mode,
				"ACRELAY_CRASH_CANONICAL="+canonical,
				"ACRELAY_CRASH_TARGET="+target,
			)
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("helper did not crash for %s: %s", mode, out)
			}
			paths, err := pendingDispatchJournals(canonical)
			if err != nil || len(paths) != 1 {
				t.Fatalf("%s crash must leave one journal: %v %v", mode, paths, err)
			}
			if _, err := Reconcile(canonical, paths[0]); err != nil {
				t.Fatalf("%s reconcile: %v", mode, err)
			}
			st, err := LoadState(canonical)
			if err != nil || len(st.Rounds) != 1 || len(st.Transactions) != 1 {
				t.Fatalf("%s must yield one canonical transaction: state=%+v err=%v", mode, st, err)
			}
			if mode == "prepared" && st.Rounds[0].Attempts[0] != string(kernel.ExecUnknown) {
				t.Fatalf("prepared crash must reconcile UNKNOWN: %+v", st.Rounds[0])
			}
			if mode != "prepared" && st.Rounds[0].Outcome != string(review.OutcomeResultValid) {
				t.Fatalf("captured crash must preserve the result: %+v", st.Rounds[0])
			}
			if pending, _ := pendingTransactions(canonical); len(pending) != 0 {
				t.Fatalf("%s reconcile left pending sidecars: %v", mode, pending)
			}
		})
	}
}

func createPreparedJournalForTest(t *testing.T, s *Session, kind string, round int) string {
	t.Helper()
	var path string
	err := withCanonicalLock(s.Canonical, func() error {
		st, err := LoadState(s.Canonical)
		if err != nil {
			return err
		}
		rev, err := store.Revision(s.Canonical)
		if err != nil {
			return err
		}
		if st.FormalRoundBound == 0 {
			st.FormalRoundBound = kernel.DefaultFormalRoundBound
			if err := appendState(s.Canonical, st,
				"\n## test formal round bound\n- value: 3\n", rev); err != nil {
				return err
			}
			rev, err = store.Revision(s.Canonical)
			if err != nil {
				return err
			}
		}
		_, p, _, err := createDispatchJournalLocked(s.Canonical, st, kind, round, 0, "fake", rev)
		path = p
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDR813PreparedCrashReconcilesUnknownIdempotently(t *testing.T) {
	s, _, _ := newSession(t, nil)
	journal := createPreparedJournalForTest(t, s, "review", 0)
	info, err := os.Stat(journal)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("prepared journal must exist as 0600: info=%v err=%v", info, err)
	}

	// Simulate crash after canonical append but before journal cleanup during
	// reconcile. The updated UNKNOWN journal remains for the next process.
	beforeJournalCleanup = func(string) error { return errors.New("simulated host crash before cleanup") }
	if _, err := Reconcile(s.Canonical, journal); err == nil || !strings.Contains(err.Error(), "cleanup") {
		t.Fatalf("first reconcile must expose the simulated cleanup crash: %v", err)
	}
	beforeJournalCleanup = nil
	t.Cleanup(func() { beforeJournalCleanup = nil })

	st, err := LoadState(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Rounds) != 1 || st.Rounds[0].Attempts[0] != string(kernel.ExecUnknown) {
		t.Fatalf("prepared crash must persist one UNKNOWN round: %+v", st.Rounds)
	}
	if _, err := Reconcile(s.Canonical, journal); err != nil {
		t.Fatalf("already-applied UNKNOWN must clean up idempotently: %v", err)
	}
	st, _ = LoadState(s.Canonical)
	if len(st.Rounds) != 1 {
		t.Fatalf("reconcile replay duplicated the UNKNOWN round: %d", len(st.Rounds))
	}
}

func TestDR813AppendBeforeCleanupReconcilesWithoutDuplicate(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{approve()})
	beforeJournalCleanup = func(string) error { return errors.New("simulated crash after canonical append") }
	if _, _, err := s.Review(context.Background(), "x", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("review must expose post-append cleanup crash: %v", err)
	}
	beforeJournalCleanup = nil
	t.Cleanup(func() { beforeJournalCleanup = nil })
	paths, _ := pendingDispatchJournals(s.Canonical)
	if len(paths) != 1 {
		t.Fatalf("post-append crash must leave one journal: %v", paths)
	}
	if _, err := Reconcile(s.Canonical, paths[0]); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(s.Canonical)
	if len(st.Rounds) != 1 || len(st.Transactions) != 1 {
		t.Fatalf("idempotent cleanup must not duplicate canonical execution: %+v", st)
	}
}

func TestDR813PendingJournalGuardsEveryMutator(t *testing.T) {
	s, _, dir := newSession(t, []adapter.FakeResult{approve()})
	journal := createPreparedJournalForTest(t, s, "review", 0)
	assertBlocked := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "pending transactions") {
			t.Fatalf("%s must be blocked by the pending journal: %v", name, err)
		}
	}
	_, _, err := s.Review(context.Background(), "x", adapter.Request{})
	assertBlocked("review", err)
	_, err = Init(s.Canonical, "new", targetPath(dir), "prior", "delta", true)
	assertBlocked("init", err)
	_, err = OpenConfirmation(s.Canonical, 0, []string{"R0-F1"})
	assertBlocked("confirmation-open", err)
	_, _, err = s.ConfirmWithReviewer(context.Background(), 0, strings.Repeat("a", 64), []string{"R0-F1"}, "delta", adapter.Request{})
	assertBlocked("confirmation-submit", err)
	_, err = Disposition(s.Canonical, "R0-F1", review.DispositionAccept, nil)
	assertBlocked("disposition", err)
	_, err = Advance(s.Canonical, "delta")
	assertBlocked("advance", err)
	_, err = Close(s.Canonical, "owner", "owner", "")
	assertBlocked("close", err)
	_, err = Terminate(s.Canonical, kernel.GovAbandoned, "owner", "stop")
	assertBlocked("terminate", err)
	status, err := Status(s.Canonical)
	if err != nil || !strings.Contains(status, "PENDING TRANSACTION") {
		t.Fatalf("status must remain available for diagnosis: %q %v", status, err)
	}
	if _, err := Reconcile(s.Canonical, journal); err != nil {
		t.Fatalf("reconcile is the allowed recovery mutation: %v", err)
	}
}

func TestDR813CorruptJournalRequiresDeclaredAbandon(t *testing.T) {
	s, _, _ := newSession(t, nil)
	journal := createPreparedJournalForTest(t, s, "review", 0)
	if err := os.WriteFile(journal, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Reconcile(s.Canonical, journal); err == nil {
		t.Fatal("corrupt journal must not reconcile")
	}
	if _, err := Terminate(s.Canonical, kernel.GovAbandoned, "owner", "manual delete"); err == nil {
		t.Fatal("ordinary terminate must not bypass a pending journal")
	}
	if _, _, err := AbandonTransaction(s.Canonical, journal, "driver", "driver", "bad"); err == nil {
		t.Fatal("non-owner/non-arbiter abandon must be refused")
	}
	st, quarantine, err := AbandonTransaction(s.Canonical, journal, "owner", "owner", "journal is corrupt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("journal must move to owner-only quarantine: %v", err)
	}
	if pending, _ := pendingTransactions(s.Canonical); len(pending) != 0 {
		t.Fatalf("declared abandon must release the guard: %v", pending)
	}
	if len(st.Transactions) != 1 || st.Transactions[0].Result != "abandoned" {
		t.Fatalf("canonical anomaly ledger missing: %+v", st.Transactions)
	}
	doc, _ := os.ReadFile(s.Canonical)
	if !strings.Contains(string(doc), "journal_digest:") || !strings.Contains(string(doc), "actor: owner") {
		t.Fatal("canonical must record journal digest and declared actor before quarantine")
	}
	if _, err := Terminate(s.Canonical, kernel.GovAbandoned, "owner", "resolved corrupt journal"); err != nil {
		t.Fatalf("normal mutation must resume after declared abandon: %v", err)
	}
}

func TestDR813ConfirmationHardCapPersistsUnknownTransaction(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{
		changesRequested("f1"),
		{TimedOut: true, TimeoutKind: adapter.TimeoutHardCap},
	})
	st, _, err := s.Review(context.Background(), "r0", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Disposition(s.Canonical, "R0-F1", review.DispositionAccept, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenConfirmation(s.Canonical, 0, []string{"R0-F1"}); err != nil {
		t.Fatal(err)
	}
	st, done, err := s.ConfirmWithReviewer(context.Background(), 0, st.TargetRevision,
		[]string{"R0-F1"}, "fixed", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if done || unknownTransaction(st) == "" {
		t.Fatalf("hard-cap confirmation must persist UNKNOWN and stay incomplete: %+v", st.Transactions)
	}
	if _, _, err := s.Review(context.Background(), "r1", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatalf("UNKNOWN confirmation must block further dispatch: %v", err)
	}
}

func TestStoreV03FailsExactVersionGate(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "legacy.md")
	st := &State{
		KernelVersion: KernelVersion, ProfileVersion: ProfileVersion, StoreVersion: "store-md v0.3",
		CollaborationID: "c", ObjectiveID: "o", TargetRevision: strings.Repeat("a", 64),
		Governance: string(kernel.GovOpen),
	}
	block, err := stateSection(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(canonical); err == nil || !strings.Contains(err.Error(), "store-md v0.4") {
		t.Fatalf("v0.3 canonical must fail the exact-version gate: %v", err)
	}
}

func TestDR813LegacyRecoverySidecarDoesNotGuardV04Canonical(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{approve()})
	legacy := s.Canonical + ".recovery-stale"
	if err := os.WriteFile(legacy, []byte("legacy-v0.2-sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Review(context.Background(), "v0.4 review", adapter.Request{}); err != nil {
		t.Fatalf("legacy sidecar must not guard a v0.4 canonical: %v", err)
	}
	if _, err := Reconcile(s.Canonical, legacy); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("legacy sidecar must not be loaded as a v0.4 journal: %v", err)
	}
}
