package adapter

import (
	"strings"
	"testing"
)

// FEAT-20260722-002 R0-CX-F1: restriction evidence is bound to
// vendor+version+GOOS+GOARCH. Evidence from one platform never admits
// another platform's real vendor dispatch.
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
	// Version mismatch stays fail-closed independent of platform.
	if err := verifyRestrictionEvidenceFor(cap, "9.9.9", "darwin", "arm64"); err == nil {
		t.Fatal("version drift must fail closed")
	}
	codex := CodexAdapter{}.Capability()
	if err := verifyRestrictionEvidenceFor(codex, codex.KnownGoodCLIVersion, "windows", "amd64"); err == nil {
		t.Fatal("codex evidence must be platform-bound too")
	}
}
