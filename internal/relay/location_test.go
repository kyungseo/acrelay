package relay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/subject"
)

func locationFixture(t *testing.T, root string) (string, subject.Spec) {
	t.Helper()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := subject.SingleFile(target)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "canonical.md"), spec
}

func TestInitPrivateLocationGateAndOneShotOverride(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, spec := locationFixture(t, root)
	policy := approvedPolicy(t)
	topology := DefaultTopologyPolicy()
	if _, err := InitSubjectTopologyWithLocation(canonical, "q", spec, "", "", false, policy, topology, nil); err == nil || !strings.Contains(err.Error(), "vcs-ancestor") {
		t.Fatalf("verified VCS signal must fail closed, got %v", err)
	}
	if _, err := os.Lstat(canonical + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("location preflight must not create a lock sidecar: %v", err)
	}
	if _, err := InitSubjectTopologyWithLocation(canonical, "q", spec, "", "", false, policy, topology,
		&LocationOverride{Actor: "owner", Rationale: "controlled private fixture"}); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	text := string(doc)
	if !strings.Contains(text, "one-shot override") || !strings.Contains(text, "controlled private fixture") || !strings.Contains(text, "vcs-ancestor") {
		t.Fatalf("canonical lacks override accountability: %s", text)
	}
	report, err := CleanupSession(CleanupRequest{Canonical: canonical, Ref: "sref-11111111111111111111111111111111", Mode: CleanupList},
		&adapter.HandleStore{Path: filepath.Join(root, "handles.json")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(report.Diagnostics, " "), "vcs-ancestor") {
		t.Fatalf("existing unsafe canonical must retain a non-blocking cleanup warning: %+v", report)
	}
}

func TestInspectPrivateLocationSupportedSyncAndOrdinaryPath(t *testing.T) {
	syncRoot := t.TempDir()
	t.Setenv("OneDrive", syncRoot)
	nested := filepath.Join(syncRoot, "private")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	signals, err := InspectPrivateLocation(filepath.Join(nested, "canonical.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got := locationSignalKinds(signals); !strings.Contains(got, "sync-onedrive") {
		t.Fatalf("supported sync root not detected: %q", got)
	}

	ordinary := t.TempDir()
	t.Setenv("OneDrive", syncRoot)
	signals, err = InspectPrivateLocation(filepath.Join(ordinary, "canonical.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(signals) != 0 {
		t.Fatalf("ordinary path got false-positive signals: %+v", signals)
	}
}
