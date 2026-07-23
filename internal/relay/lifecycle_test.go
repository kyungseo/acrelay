package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/platform"
	"github.com/kyungseo/acrelay/internal/store"
)

func lifecycleFixture(t *testing.T) (string, *adapter.HandleStore, string, string) {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(dir, "canonical.md")
	if _, err := Init(canonical, "q", target, "", "", false, approvedPolicy(t)); err != nil {
		t.Fatal(err)
	}
	handles := &adapter.HandleStore{Path: filepath.Join(dir, "handles.json")}
	root, err := handles.NeutralRuntimeRoot()
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := platform.MkdirTempPrivateAt(root, "acrelay-review-root-")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := handles.Register("fake", "fixture-handle-001", "review-input-trust-v1/neutral", cwd)
	if err != nil {
		t.Fatal(err)
	}
	err = withCanonicalLock(canonical, func() error {
		st, err := LoadState(canonical)
		if err != nil {
			return err
		}
		rev, err := store.Revision(canonical)
		if err != nil {
			return err
		}
		st.SessionRef, st.Vendor = ref, "fake"
		return appendState(canonical, st, "\n## lifecycle fixture session bound\n", rev)
	})
	if err != nil {
		t.Fatal(err)
	}
	return canonical, handles, ref, cwd
}

func TestCleanupSessionListDryRunApplyAndRepeat(t *testing.T) {
	canonical, handles, ref, cwd := lifecycleFixture(t)
	if _, err := Terminate(canonical, "ABANDONED", "owner", "fixture terminal"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []CleanupMode{CleanupList, CleanupDryRun} {
		report, err := CleanupSession(CleanupRequest{Canonical: canonical, Ref: ref, Mode: mode}, handles)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Blocked) != 0 || report.Applied || len(report.Actions) != 2 {
			t.Fatalf("unexpected %s report: %+v", mode, report)
		}
		if _, _, _, _, err := handles.Lookup(ref); err != nil {
			t.Fatalf("%s must not mutate handle: %v", mode, err)
		}
	}
	report, err := CleanupSession(CleanupRequest{Canonical: canonical, Ref: ref, Mode: CleanupApply, Actor: "owner", Reason: "no related objective will resume"}, handles)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Applied || report.AlreadyClean {
		t.Fatalf("apply did not execute: %+v", report)
	}
	if _, err := os.Lstat(cwd); !os.IsNotExist(err) {
		t.Fatalf("owned cwd still exists: %v", err)
	}
	if _, _, _, _, err := handles.Lookup(ref); err == nil {
		t.Fatal("handle mapping still exists")
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("raw canonical must remain owner-retained: %v", err)
	}
	doc, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), cleanupMarkerLabel(ref)) || !strings.Contains(string(doc), "continuity: abandoned") {
		t.Fatal("canonical cleanup accountability marker missing")
	}
	repeated, err := CleanupSession(CleanupRequest{Canonical: canonical, Ref: ref, Mode: CleanupApply, Actor: "owner", Reason: "repeat"}, handles)
	if err != nil {
		t.Fatal(err)
	}
	if !repeated.AlreadyClean || repeated.Applied {
		t.Fatalf("repeated cleanup must converge without another mutation: %+v", repeated)
	}
}

func TestCleanupSessionBlocksNonterminalAndPending(t *testing.T) {
	canonical, handles, ref, _ := lifecycleFixture(t)
	report, err := CleanupSession(CleanupRequest{Canonical: canonical, Ref: ref, Mode: CleanupApply, Actor: "owner", Reason: "premature"}, handles)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Blocked) == 0 || !strings.Contains(strings.Join(report.Blocked, " "), "nonterminal") {
		t.Fatalf("nonterminal cleanup not blocked: %+v", report)
	}
	if _, err := Terminate(canonical, "ABANDONED", "owner", "fixture terminal"); err != nil {
		t.Fatal(err)
	}
	pending := canonical + ".dispatch-tx-11111111111111111111111111111111.json"
	if err := platform.WritePrivateFile(pending, []byte("fixture pending")); err != nil {
		t.Fatal(err)
	}
	report, err = CleanupSession(CleanupRequest{Canonical: canonical, Ref: ref, Mode: CleanupApply, Actor: "owner", Reason: "pending"}, handles)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Blocked) == 0 || !strings.Contains(strings.Join(report.Blocked, " "), "pending") {
		t.Fatalf("pending journal cleanup not blocked: %+v", report)
	}
	if _, _, _, _, err := handles.Lookup(ref); err != nil {
		t.Fatalf("blocked cleanup mutated handle: %v", err)
	}
}

func TestCleanupSessionDiagnosesMissingAndLegacyCwd(t *testing.T) {
	canonical, handles, ref, cwd := lifecycleFixture(t)
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := Terminate(canonical, "ABANDONED", "owner", "fixture terminal"); err != nil {
		t.Fatal(err)
	}
	report, err := CleanupSession(CleanupRequest{Canonical: canonical, Ref: ref, Mode: CleanupList}, handles)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(report.Diagnostics, " "), "already missing") {
		t.Fatalf("missing cwd diagnostic absent: %+v", report)
	}
	status, err := StatusWithHandles(canonical, handles)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "bound cwd missing") || strings.Contains(status, cwd) {
		t.Fatalf("status lifecycle diagnostic is missing or leaked a private path: %s", status)
	}
}

func TestCleanupQuarantineRequiresDispositionAndIsRepeatable(t *testing.T) {
	canonical, _, _, _ := lifecycleFixture(t)
	if err := withCanonicalLock(canonical, func() error {
		st, err := LoadState(canonical)
		if err != nil {
			return err
		}
		rev, err := store.Revision(canonical)
		if err != nil {
			return err
		}
		st.FormalRoundBound = 3
		return appendState(canonical, st, "\n## lifecycle fixture round bound\n", rev)
	}); err != nil {
		t.Fatal(err)
	}
	txID := "tx-22222222222222222222222222222222"
	journal := canonical + ".dispatch-" + txID + ".json"
	if err := platform.WritePrivateFile(journal, []byte("unreconcilable fixture")); err != nil {
		t.Fatal(err)
	}
	_, quarantine, err := AbandonTransaction(canonical, journal, "owner", "owner", "fixture cannot reconcile")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []CleanupMode{CleanupList, CleanupDryRun} {
		report, err := CleanupQuarantine(SidecarCleanupRequest{Canonical: canonical, Path: quarantine, Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Blocked) != 0 || report.Applied {
			t.Fatalf("unexpected quarantine %s report: %+v", mode, report)
		}
		if _, err := os.Stat(quarantine); err != nil {
			t.Fatalf("non-apply quarantine cleanup mutated file: %v", err)
		}
	}
	report, err := CleanupQuarantine(SidecarCleanupRequest{Canonical: canonical, Path: quarantine, Mode: CleanupApply, Actor: "owner", Reason: "durable disposition retained"})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Applied {
		t.Fatalf("quarantine purge not applied: %+v", report)
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatalf("quarantine still exists: %v", err)
	}
	repeat, err := CleanupQuarantine(SidecarCleanupRequest{Canonical: canonical, Path: quarantine, Mode: CleanupApply, Actor: "owner", Reason: "repeat"})
	if err != nil {
		t.Fatal(err)
	}
	if !repeat.AlreadyClean || repeat.Applied {
		t.Fatalf("repeated quarantine cleanup must converge: %+v", repeat)
	}
}

func TestCleanupOrphanSidecarIsExactAndLockStaysBlocked(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "missing.md")
	orphan := canonical + ".dispatch-tx-33333333333333333333333333333333.json"
	if err := platform.WritePrivateFile(orphan, []byte("orphan fixture")); err != nil {
		t.Fatal(err)
	}
	report, err := CleanupOrphanSidecar(SidecarCleanupRequest{Canonical: canonical, Path: orphan, Mode: CleanupApply, Actor: "owner", Reason: "canonical was intentionally removed"})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Applied {
		t.Fatalf("exact orphan cleanup not applied: %+v", report)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan sidecar still exists: %v", err)
	}
	lock := canonical + ".lock"
	if err := platform.WritePrivateFile(lock, nil); err != nil {
		t.Fatal(err)
	}
	blocked, err := CleanupOrphanSidecar(SidecarCleanupRequest{Canonical: canonical, Path: lock, Mode: CleanupApply, Actor: "owner", Reason: "looks stale"})
	if err != nil {
		t.Fatal(err)
	}
	if len(blocked.Blocked) == 0 || blocked.Applied {
		t.Fatalf("unproven lock deletion must stay blocked: %+v", blocked)
	}
}
