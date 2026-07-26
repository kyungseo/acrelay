package adapter

import (
	"context"
	"github.com/kyungseo/acrelay/internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Restriction evidence is platform-bound, while supported CLI versions use a
// minimum floor and must pass their required capability probe in Prepare.
func TestRestrictionEvidenceIsPlatformBound(t *testing.T) {
	cap := ClaudeAdapter{}.Capability()
	if err := verifyRestrictionEvidenceFor(cap, cap.KnownGoodCLIVersion, "darwin", "arm64"); err != nil {
		t.Fatalf("verified platform must pass: %v", err)
	}
	for _, host := range [][2]string{{"windows", "amd64"}, {"windows", "arm64"}, {"linux", "amd64"}} {
		err := verifyRestrictionEvidenceFor(cap, cap.KnownGoodCLIVersion, host[0], host[1])
		if err == nil || !strings.Contains(err.Error(), "no verified restriction evidence on "+host[0]+"/"+host[1]) {
			t.Fatalf("unverified platform %s/%s must fail closed with an actionable diagnostic, got: %v", host[0], host[1], err)
		}
		if !strings.Contains(err.Error(), "unrestricted fallback forbidden") {
			t.Fatalf("diagnostic must forbid fallback: %v", err)
		}
	}
	if err := verifyRestrictionEvidenceFor(cap, "2.1.220", "darwin", "arm64"); err != nil {
		t.Fatalf("newer version must pass the floor on a verified platform: %v", err)
	}
	if err := verifyRestrictionEvidenceFor(cap, "2.1.216", "darwin", "arm64"); err == nil {
		t.Fatal("version below the supported floor must fail closed")
	}
	codex := CodexAdapter{}.Capability()
	if err := verifyRestrictionEvidenceFor(codex, codex.KnownGoodCLIVersion, "windows", "amd64"); err == nil {
		t.Fatal("codex evidence must be platform-bound too")
	}
}

// TR-CX-N1 black-box fixture: on an unverified platform, the REAL adapter's
// Prepare fails closed with the platform diagnostic — no seam set, version
// probe reachable, and the failure fires before any model-bearing dispatch.
// Runs only on unverified lanes (Linux/Windows CI); the verified platform
// legitimately admits dispatch and is covered by the restriction spike.
func TestPrepareFailsClosedOnUnverifiedPlatformWithoutSeam(t *testing.T) {
	host := runtime.GOOS + "/" + runtime.GOARCH
	for _, p := range (ClaudeAdapter{}).Capability().KnownGoodPlatforms {
		if p == host {
			t.Skipf("verified platform %s: the gate admits real dispatch here by design", host)
		}
	}
	dir := t.TempDir()
	testenv.InstallFakeVendor(t, filepath.Join(dir, "claude"),
		`if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
exit 99
`)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	handles := &HandleStore{Path: filepath.Join(dir, "handles.json")}
	_, err := ClaudeAdapter{}.Prepare(context.Background(), approvedTestRequest(t, Request{
		Prompt: "review", SchemaJSON: `{"type":"object"}`,
	}), handles)
	if err == nil || !strings.Contains(err.Error(), "no verified restriction evidence on "+host) {
		t.Fatalf("unverified platform must fail closed with the platform diagnostic before dispatch, got: %v", err)
	}
}
