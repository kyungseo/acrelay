package relay

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/testenv"
)

// R2-CX-F3 item 1: a real Claude child returning the resume-not-found
// envelope must produce a consuming FAILED review transaction with the
// resume-handle-invalid cause — driven through relay.Session.Review with the
// production ClaudeAdapter, not a FakeAdapter.
// skipUnlessVendorPlatformVerified skips real-adapter dispatch tests on
// platforms outside the restriction-evidence set (FEAT-20260722-002 F1): the
// production gate fail-closes Prepare there by design, and the classification
// logic under test is platform-independent (covered on the verified lane).
func skipUnlessVendorPlatformVerified(t *testing.T) {
	t.Helper()
	host := runtime.GOOS + "/" + runtime.GOARCH
	for _, p := range (adapter.ClaudeAdapter{}).Capability().KnownGoodPlatforms {
		if p == host {
			return
		}
	}
	t.Skipf("capability: real-adapter dispatch is restriction-gated on %s (verified platforms only)", host)
}

func TestRealClaudeResumeNotFoundConsumesReviewAttempt(t *testing.T) {
	skipUnlessVendorPlatformVerified(t)
	dir := t.TempDir()
	// A fake `claude` that succeeds on the initial (new-session) dispatch and
	// returns the resume-not-found error envelope whenever --resume is passed.
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format stream-json --verbose --json-schema --resume --safe-mode --add-dir --tools --permission-mode --system-prompt'; exit 0; fi
for a in "$@"; do
  if [ "$a" = "--resume" ]; then
    printf '%s\n' '{"type":"result","subtype":"success","is_error":true,"result":"No conversation found with session ID","session_id":"11111111-2222-3333-4444-555555555555"}'
    exit 1
  fi
done
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"11111111-2222-3333-4444-555555555555","structured_output":{"verdict":"approve","examined":[{"id":"E1","member":"target.go","location":{"kind":"text-lines","start":1,"end":1},"excerpt":"func greet() string { return \"hello\" }","claim":"examined"}],"findings":[],"approval_requests":[]},"modelUsage":{"claude-test":{}}}'
`
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	testenv.InstallFakeVendor(t, filepath.Join(bin, "claude"), script)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	target := filepath.Join(dir, "target.go")
	if err := os.WriteFile(target, []byte(`func greet() string { return "hello" }`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Session{
		Adapter:   adapter.ClaudeAdapter{},
		Handles:   &adapter.HandleStore{Path: filepath.Join(dir, "handles.json")},
		Canonical: filepath.Join(dir, "canonical.md"),
	}
	if _, err := Init(s.Canonical, "ready?", target, "", "", false, approvedPolicy(t)); err != nil {
		t.Fatal(err)
	}
	// R0: new session, succeeds and stores the resume handle.
	if _, outcome, err := s.Review(context.Background(), "r0", adapter.Request{}); err != nil || outcome != "result-valid" {
		t.Fatalf("initial real-claude review must succeed: outcome=%s err=%v", outcome, err)
	}
	// R1: resumes the stored session; the fake returns resume-not-found.
	st, outcome, err := s.Review(context.Background(), "r1", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" {
		t.Fatalf("resume-not-found must be a FAILED review outcome: %s", outcome)
	}
	last := st.Rounds[len(st.Rounds)-1]
	if last.Attempts[len(last.Attempts)-1] != "FAILED" {
		t.Fatalf("resume-not-found must consume the attempt as FAILED: %+v", last)
	}
	tx := st.Transactions[len(st.Transactions)-1]
	if tx.Execution != "FAILED" || tx.CauseCode != adapter.CauseResumeHandleInvalid ||
		tx.CauseSource != adapter.CauseSourceInferred {
		t.Fatalf("resume-not-found cause must persist as inferred resume-handle-invalid: %+v", tx)
	}
	// The private status surface must render the allowlisted phrase.
	out, err := Status(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "last failure cause: "+adapter.CauseResumeHandleInvalid) {
		t.Fatalf("status must surface the resume-handle-invalid cause:\n%s", out)
	}
}
