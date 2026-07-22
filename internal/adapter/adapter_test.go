package adapter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kyungseo/acrelay/internal/platform"
	"github.com/kyungseo/acrelay/internal/testenv"
)

func TestValidateEffort(t *testing.T) {
	cap := ClaudeAdapter{}.Capability()
	if err := ValidateEffort(cap, ""); err != nil {
		t.Fatal("omitted effort must pass (no flag sent)")
	}
	if err := ValidateEffort(cap, "low"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEffort(cap, "minimal"); err == nil {
		t.Fatal("minimal is codex-only: explicit unsupported effort must fail pre-dispatch")
	}
	if err := ValidateEffort(CodexAdapter{}.Capability(), "minimal"); err != nil {
		t.Fatal("codex supports minimal")
	}
}

func approvedTestPolicy(t *testing.T) TrustPolicy {
	t.Helper()
	p, err := NewTrustPolicy("test-owner", true, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func approvedTestRequest(t *testing.T, req Request) Request {
	t.Helper()
	req.TrustPolicy = approvedTestPolicy(t)
	if req.SubjectRoot == "" {
		base := req.WorkingDir
		if base == "" {
			base = t.TempDir()
		}
		req.SubjectRoot = filepath.Join(base, "subject-root")
		if err := os.MkdirAll(req.SubjectRoot, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	req.WorkingDir = ""
	return req
}

func testHandleWorkingDir(t *testing.T) string {
	t.Helper()
	dir, err := platform.MkdirTempPrivate("acrelay-review-root-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func hasArgSequence(args []string, want ...string) bool {
	for i := 0; i+len(want) <= len(args); i++ {
		match := true
		for j := range want {
			if args[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestTrustPolicyApprovalContract(t *testing.T) {
	if _, err := NewTrustPolicy("owner", false, false); err == nil || !strings.Contains(err.Error(), EgressApprovalID) {
		t.Fatalf("missing egress approval must fail closed: %v", err)
	}
	if _, err := NewTrustPolicy("", true, false); err == nil {
		t.Fatal("approval actor is required")
	}
	neutral := approvedTestPolicy(t)
	unsafe, err := NewTrustPolicy("test-owner", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if neutral.ProfileID == unsafe.ProfileID || unsafe.WorkingDirMode != WorkingDirInTarget {
		t.Fatalf("execution modes must have distinct stable profile identities: neutral=%+v unsafe=%+v", neutral, unsafe)
	}
	tampered := neutral
	tampered.ProfileID = unsafe.ProfileID
	if err := tampered.Validate(); err == nil {
		t.Fatal("profile/mode mismatch must fail closed")
	}
	unknown := neutral
	unknown.Approvals = append(unknown.Approvals, ApprovalRecord{
		ID: "future-owner-gate", Actor: "test-owner", Decision: "approved", Scope: "future bounded scope",
	})
	if err := unknown.Validate(); err != nil {
		t.Fatalf("well-formed future approvals must remain preservable: %v", err)
	}
}

func TestPrepareExecutionRootModes(t *testing.T) {
	subjectRoot := filepath.Join(t.TempDir(), "subject")
	if err := os.MkdirAll(subjectRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	neutral := approvedTestPolicy(t)
	prepared, cleanup, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, TrustPolicy: neutral}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup == "" || prepared.WorkingDir != cleanup {
		t.Fatalf("default neutral mode must create an owned temp cwd: req=%+v cleanup=%q", prepared, cleanup)
	}
	if inside, err := pathWithin(subjectRoot, cleanup); err != nil || inside {
		t.Fatalf("neutral cwd must be outside subject: inside=%v err=%v", inside, err)
	}
	if err := platform.VerifyPrivateDir(cleanup); err != nil {
		t.Fatalf("neutral cwd must be owner-only: %v", err)
	}
	resumed, resumedCleanup, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, TrustPolicy: neutral}, cleanup)
	if err != nil || resumed.WorkingDir != cleanup || resumedCleanup != "" {
		t.Fatalf("neutral resume must reuse the handle-bound cwd without taking cleanup ownership: req=%+v cleanup=%q err=%v", resumed, resumedCleanup, err)
	}
	if _, _, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, WorkingDir: cleanup, TrustPolicy: neutral}, cleanup); err == nil {
		t.Fatal("caller-supplied neutral cwd must stay forbidden even when it names the stored cwd")
	}
	if err := os.RemoveAll(cleanup); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), cleanup); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, TrustPolicy: neutral}, cleanup); err == nil {
		t.Fatal("retargeting a stored neutral cwd to a symlink must fail closed")
	}
	if err := os.Remove(cleanup); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, WorkingDir: subjectRoot, TrustPolicy: neutral}, ""); err == nil {
		t.Fatal("neutral profile must reject caller-supplied cwd")
	}
	if _, _, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, WorkingDir: t.TempDir(), TrustPolicy: neutral}, ""); err == nil {
		t.Fatal("neutral profile must reject even an external caller-supplied cwd")
	}
	unsafe, err := NewTrustPolicy("test-owner", true, true)
	if err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err = prepareExecutionRoot(Request{SubjectRoot: subjectRoot, TrustPolicy: unsafe}, "")
	if err != nil || cleanup != "" || prepared.WorkingDir != subjectRoot {
		t.Fatalf("approved in-target mode must use subject root: req=%+v cleanup=%q err=%v", prepared, cleanup, err)
	}
	outside := t.TempDir()
	if _, _, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, WorkingDir: outside, TrustPolicy: unsafe}, ""); err == nil {
		t.Fatal("in-target profile must reject an outside cwd")
	}
	boundInTarget := filepath.Join(subjectRoot, "reviewer-cwd")
	if err := os.Mkdir(boundInTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if resumed, _, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, TrustPolicy: unsafe}, boundInTarget); err != nil || resumed.WorkingDir != boundInTarget {
		t.Fatalf("in-target resume must reuse its handle-bound cwd: req=%+v err=%v", resumed, err)
	}
	if _, _, err := prepareExecutionRoot(Request{SubjectRoot: subjectRoot, WorkingDir: subjectRoot, TrustPolicy: unsafe}, boundInTarget); err == nil {
		t.Fatal("in-target resume must reject a different caller cwd without explicit reset")
	}
	resolvedTemp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareExecutionRoot(Request{SubjectRoot: resolvedTemp, TrustPolicy: neutral}, ""); err == nil ||
		!strings.Contains(err.Error(), "neutral reviewer cwd unavailable") {
		t.Fatalf("subject containing the system temp root must fail closed: %v", err)
	}
}

func TestHandleStoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	h := &HandleStore{Path: filepath.Join(dir, "handles.json")}

	profile := profileID(WorkingDirNeutral)
	workdir1 := testHandleWorkingDir(t)
	ref1, err := h.Register("claude", "11111111-2222-3333-4444-555555555555", profile, workdir1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ref1, "11111111-2222-3333-4444-555555555555") {
		t.Fatal("session_ref must not embed the native handle")
	}
	// private from creation (0600 on POSIX, protected DACL on Windows)
	if err := platform.VerifyPrivateFile(h.Path); err != nil {
		t.Fatalf("handle store must be private from creation: %v", err)
	}
	// merge preserved
	workdir2 := testHandleWorkingDir(t)
	ref2, _ := h.Register("codex", "thread-2", profile, workdir2)
	if _, nh, storedProfile, storedWorkingDir, err := h.Lookup(ref1); err != nil || nh != "11111111-2222-3333-4444-555555555555" || storedProfile != profile || storedWorkingDir != workdir1 {
		t.Fatalf("first entry lost after second register: %v", err)
	}
	// randomness: two registrations of the same handle produce distinct refs
	ref3, _ := h.Register("claude", "11111111-2222-3333-4444-555555555555", profile, testHandleWorkingDir(t))
	if ref1 == ref3 {
		t.Fatal("references must be random, not derived from the handle")
	}
	// rotation: old ref removed, new ref resolves
	ref4, err := h.Rotate(ref2, "codex", "thread-2b", profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := h.Lookup(ref2); err == nil {
		t.Fatal("rotated-away ref must fail closed")
	}
	if _, nh, storedProfile, storedWorkingDir, _ := h.Lookup(ref4); nh != "thread-2b" || storedProfile != profile || storedWorkingDir != workdir2 {
		t.Fatal("rotated ref must resolve to the new handle")
	}
	// deletion
	if err := h.Delete(ref4); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workdir2); !os.IsNotExist(err) {
		t.Fatalf("neutral handle deletion must remove its owned working directory: %v", err)
	}
	if _, _, _, _, err := h.Lookup(ref4); err == nil {
		t.Fatal("deleted ref must fail closed")
	}
	// missing ref fails closed (no silent new-session fallback)
	if _, _, _, _, err := h.Lookup("sref-doesnotexist"); err == nil ||
		!strings.Contains(err.Error(), "no silent new-session fallback") {
		t.Fatalf("missing ref must fail closed with explicit contract: %v", err)
	}
}

func TestHandleStoreCorruptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	h := &HandleStore{Path: filepath.Join(dir, "handles.json")}
	if err := platform.WritePrivateFile(h.Path, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Register("claude", "x", profileID(WorkingDirNeutral), testHandleWorkingDir(t)); err == nil {
		t.Fatal("corrupt store must fail closed, never be overwritten")
	}
	if b, _ := os.ReadFile(h.Path); string(b) != "{not json" {
		t.Fatal("corrupt store content must remain untouched")
	}
	// unsupported version fails closed
	if err := platform.WritePrivateFile(h.Path, []byte(`{"version":1,"entries":{"legacy":{"vendor":"claude","handle":"native"}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := (&HandleStore{Path: h.Path}).Lookup("legacy"); err == nil ||
		!strings.Contains(err.Error(), "version 1 unsupported") {
		t.Fatalf("handle store v1 must fail closed: %v", err)
	}
	if err := platform.WritePrivateFile(h.Path, []byte(`{"version":99,"entries":{}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := (&HandleStore{Path: h.Path}).Lookup("any"); err == nil ||
		!strings.Contains(err.Error(), "version") {
		t.Fatalf("version mismatch must fail closed: %v", err)
	}
	if err := platform.WritePrivateFile(h.Path, []byte(`{"version":2,"entries":{"cwd-less":{"vendor":"claude","handle":"native","profile_id":"review-input-trust-v1/neutral"}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := (&HandleStore{Path: h.Path}).Lookup("cwd-less"); err == nil ||
		!strings.Contains(err.Error(), "working directory") {
		t.Fatalf("handle store v2 entry without bound cwd must fail closed: %v", err)
	}
}

func TestParseClaudeEnvelope(t *testing.T) {
	clean := `{"type":"result","subtype":"success","is_error":false,"result":"OK","session_id":"s1","structured_output":{"verdict":"approve","findings":["f"]},"modelUsage":{"claude-haiku-4-5-20251001":{}}}`
	env, diag, err := parseClaudeEnvelope([]byte(clean))
	if err != nil || diag != "" || env.SessionID != "s1" {
		t.Fatalf("clean envelope: env=%v diag=%q err=%v", env, diag, err)
	}
	// warning prefix → explicit diagnostic, envelope still parsed
	prefixed := "Warning: something\n" + clean
	env, diag, err = parseClaudeEnvelope([]byte(prefixed))
	if err != nil || diag == "" {
		t.Fatalf("prefixed envelope must parse with diagnostic: diag=%q err=%v", diag, err)
	}
	if env.Structured["verdict"] != "approve" {
		t.Fatal("structured_output lost")
	}
	// garbage fails closed
	if _, _, err := parseClaudeEnvelope([]byte("no json here")); err == nil {
		t.Fatal("non-JSON stdout must fail closed")
	}
	// truncated/malformed JSON after prefix fails closed
	if _, _, err := parseClaudeEnvelope([]byte("Warning\n{\"type\":")); err == nil {
		t.Fatal("malformed envelope must fail closed")
	}
}

func TestParseCodexJSONL(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t-1"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"{\"verdict\":\"approve\",\"findings\":[\"f\"]}"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1}}`,
	}, "\n")
	cap, err := parseCodexJSONL([]byte(stream))
	if err != nil {
		t.Fatal(err)
	}
	if cap.ThreadID != "t-1" || cap.AgentMessage == "" {
		t.Fatalf("capture incomplete: %+v", cap)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(cap.AgentMessage), &m); err != nil || m["verdict"] != "approve" {
		t.Fatal("agent_message must carry the schema-enforced JSON")
	}

	// turn.failed captured
	failed := `{"type":"thread.started","thread_id":"t-2"}` + "\n" +
		`{"type":"turn.failed","error":{"message":"boom"}}`
	cap, err = parseCodexJSONL([]byte(failed))
	if err != nil || !cap.TurnFailed {
		t.Fatalf("turn.failed must be captured: %+v err=%v", cap, err)
	}

	// malformed event line fails closed (DR-811 이관 fixture)
	if _, err := parseCodexJSONL([]byte("{\"type\":\"thread.started\"\nnot-json")); err == nil {
		t.Fatal("malformed JSONL must fail closed")
	}
	// non-UTF8 bytes in stream: invalid JSON → fail closed, not skipped
	if _, err := parseCodexJSONL([]byte{0xff, 0xfe, 0x0a}); err == nil {
		t.Fatal("non-UTF8 garbage must fail closed")
	}
}

func TestDefaultTimeoutsStructure(t *testing.T) {
	d := DefaultTimeouts()
	if d.Startup <= 0 || d.Idle <= 0 || d.HardCap <= 0 || d.Grace <= 0 {
		t.Fatal("all timeout components must have positive defaults")
	}
	if d.HardCap <= d.Idle || d.Idle <= d.Grace {
		t.Fatal("timeout ordering must be grace < idle < hard-cap")
	}
}

// PATCH-002: version equality is evidence-only; observability remains the
// pre-dispatch hard gate.
func TestPreflightVersionObservability(t *testing.T) {
	cap := ClaudeAdapter{}.Capability()
	if err := PreflightVersion(cap, "2.1.217"); err != nil {
		t.Fatal(err)
	}
	if err := PreflightVersion(cap, "9.9.9"); err != nil {
		t.Fatalf("changed observable version must not be an admission gate: %v", err)
	}
	if err := PreflightVersion(cap, ""); err == nil {
		t.Fatal("unobservable version must fail closed")
	}
}

func TestVersionProbeIgnoresStderrWarnings(t *testing.T) {
	script := `#!/bin/sh
echo 'WARNING: local setup warning' >&2
echo 'codex-cli 0.144.1'
`
	_, _ = installAdapterCLI(t, "codex", script)
	version, banner, err := detectCodexVersion(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if version != "0.144.1" || banner != "codex-cli 0.144.1" {
		t.Fatalf("stderr warning polluted observed version: version=%q banner=%q", version, banner)
	}
}

// skipWithoutPOSIXSignalDeath skips fixtures that rely on signal-death wait
// status — a POSIX-only capability with no Windows runtime equivalent
// (FEAT-20260722-002 R0-CX-F8: every skip carries its capability reason).
func skipWithoutPOSIXSignalDeath(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("capability: POSIX signal-death classification has no Windows runtime equivalent (terminatedBySignal is documented false)")
	}
}

func installAdapterCLI(t *testing.T, name, script string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	cli := filepath.Join(dir, name)
	testenv.InstallFakeVendor(t, cli, script)
	// Fake-CLI fixture context: opt the current platform into the
	// restriction-evidence gate (test seam — see verifyRestrictionEvidenceFor).
	// The gate's own contract is pinned by TestRestrictionEvidenceIsPlatformBound.
	origExtra := extraRestrictionPlatforms
	extraRestrictionPlatforms = []string{runtime.GOOS + "/" + runtime.GOARCH}
	t.Cleanup(func() { extraRestrictionPlatforms = origExtra })
	logPath := filepath.Join(dir, "calls.log")
	t.Setenv("ACRELAY_TEST_LOG", logPath)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir, logPath
}

func TestRestrictedAdapterCommandSurfaceInitialAndResume(t *testing.T) {
	tests := []struct {
		name   string
		script string
		make   func() Adapter
		check  func(*testing.T, PreparedInvocation, bool)
	}{
		{
			name: "claude",
			script: `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema --resume --safe-mode --add-dir --tools --permission-mode --system-prompt'; exit 0; fi
exit 99
`,
			make: func() Adapter { return ClaudeAdapter{} },
			check: func(t *testing.T, invocation PreparedInvocation, resume bool) {
				p := invocation.(*preparedClaude)
				if !hasArgSequence(p.args, "--safe-mode") ||
					!hasArgSequence(p.args, "--add-dir", p.req.SubjectRoot) ||
					!hasArgSequence(p.args, "--strict-mcp-config") ||
					!hasArgSequence(p.args, "--mcp-config", `{"mcpServers":{}}`) ||
					!hasArgSequence(p.args, "--tools", "Read,Glob,Grep") ||
					!hasArgSequence(p.args, "--permission-mode", "dontAsk") ||
					!hasArgSequence(p.args, "--system-prompt", ReviewerTrustSystemPrompt) {
					t.Fatalf("Claude restriction argv incomplete: %q", p.args)
				}
				joined := strings.Join(p.args, " ")
				if strings.Contains(joined, "Write") || strings.Contains(joined, "Edit") || strings.Contains(joined, "Bash") {
					t.Fatalf("Claude write-capable tool leaked into argv: %q", p.args)
				}
				if hasArgSequence(p.args, "--resume", "11111111-2222-3333-4444-555555555555") != resume {
					t.Fatalf("Claude resume argv mismatch: resume=%v args=%q", resume, p.args)
				}
			},
		},
		{
			name: "codex",
			script: `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then echo '{"checks":{"config.load":{"status":"ok","details":{"model":"test-model","provider":"test"}}}}'; exit 0; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema --ignore-user-config --ignore-rules --strict-config --sandbox'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
exit 99
`,
			make: func() Adapter { return CodexAdapter{} },
			check: func(t *testing.T, invocation PreparedInvocation, resume bool) {
				p := invocation.(*preparedCodex)
				if !hasArgSequence(p.args, "exec", "--ignore-user-config", "--ignore-rules", "--strict-config", "--sandbox", "read-only", "--skip-git-repo-check", "--json") {
					t.Fatalf("Codex restriction argv incomplete: %q", p.args)
				}
				if hasArgSequence(p.args, "--dangerously-bypass-approvals-and-sandbox") {
					t.Fatalf("Codex unrestricted fallback leaked into argv: %q", p.args)
				}
				if hasArgSequence(p.args, "resume", "native-session-fixture") != resume {
					t.Fatalf("Codex resume argv mismatch: resume=%v args=%q", resume, p.args)
				}
				if p.args[len(p.args)-1] != "-" {
					t.Fatalf("Codex prompt must remain stdin positional: %q", p.args)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := installAdapterCLI(t, tc.name, tc.script)
			handles := &HandleStore{Path: filepath.Join(dir, "handles.json")}
			base := approvedTestRequest(t, Request{Prompt: "review", SchemaJSON: `{"type":"object"}`})
			initial, err := tc.make().Prepare(context.Background(), base, handles)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, initial, false)
			initialCwd := ""
			switch p := initial.(type) {
			case *preparedClaude:
				initialCwd = p.req.WorkingDir
			case *preparedCodex:
				initialCwd = p.req.WorkingDir
			}
			if err := initial.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(initialCwd); !os.IsNotExist(err) {
				t.Fatalf("neutral cwd must be removed by Close: %s err=%v", initialCwd, err)
			}
			resumeWorkingDir := testHandleWorkingDir(t)
			nativeFixture := "native-session-fixture"
			if tc.name == "claude" {
				nativeFixture = "11111111-2222-3333-4444-555555555555"
			}
			ref, err := handles.Register(tc.name, nativeFixture, base.TrustPolicy.ProfileID, resumeWorkingDir)
			if err != nil {
				t.Fatal(err)
			}
			base.ResumeRef = ref
			resumed, err := tc.make().Prepare(context.Background(), base, handles)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, resumed, true)
			switch p := resumed.(type) {
			case *preparedClaude:
				if p.req.WorkingDir != resumeWorkingDir {
					t.Fatalf("Claude resume must reuse handle-bound cwd: got=%q want=%q", p.req.WorkingDir, resumeWorkingDir)
				}
			case *preparedCodex:
				if p.req.WorkingDir != resumeWorkingDir {
					t.Fatalf("Codex resume must reuse handle-bound cwd: got=%q want=%q", p.req.WorkingDir, resumeWorkingDir)
				}
			}
			if err := resumed.Close(); err != nil {
				t.Fatal(err)
			}
			unsafe, err := NewTrustPolicy("test-owner", true, true)
			if err != nil {
				t.Fatal(err)
			}
			base.TrustPolicy = unsafe
			if prepared, err := tc.make().Prepare(context.Background(), base, handles); err == nil || prepared != nil ||
				!strings.Contains(err.Error(), "trust profile mismatch") {
				t.Fatalf("resume under a different trust profile must fail closed: prepared=%v err=%v", prepared, err)
			}
		})
	}
}

func TestRestrictionVersionDriftBlocksBeforeDispatch(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		adapter Adapter
	}{
		{
			name: "claude",
			script: `#!/bin/sh
echo "$@" >> "$ACRELAY_TEST_LOG"
if [ "$1" = "--version" ]; then echo "9.9.9 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo "reformatted capability reference"; exit 0; fi
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"session-new","structured_output":{"verdict":"approve","findings":[]},"modelUsage":{"claude-default":{}}}'
`,
			adapter: ClaudeAdapter{},
		},
		{
			name: "codex",
			script: `#!/bin/sh
echo "$@" >> "$ACRELAY_TEST_LOG"
if [ "$1" = "--version" ]; then echo "codex-cli 9.9.9"; exit 0; fi
if [ "$1" = "doctor" ]; then
  echo '{"checks":{"config.load":{"status":"ok","details":{"model":"codex-default","provider":"openai","private_path":"/must/not/persist"}}}}'
  exit 0
fi
if [ "$2" = "--help" ] || [ "$3" = "--help" ]; then echo "reformatted capability reference"; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-new"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"approve\",\"findings\":[]}"}}'
printf '%s\n' '{"type":"turn.completed"}'
`,
			adapter: CodexAdapter{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, logPath := installAdapterCLI(t, tc.name, tc.script)
			prepared, err := tc.adapter.Prepare(context.Background(), approvedTestRequest(t, Request{
				Prompt: "review", SchemaJSON: `{"type":"object"}`, WorkingDir: dir,
			}), &HandleStore{Path: filepath.Join(dir, "handles.json")})
			if err == nil || prepared != nil || !strings.Contains(err.Error(), "owner gate required") ||
				!strings.Contains(err.Error(), "unrestricted fallback forbidden") {
				t.Fatalf("version drift must fail before dispatch: prepared=%v err=%v", prepared, err)
			}
			calls, _ := os.ReadFile(logPath)
			if !strings.Contains(string(calls), "--version") || strings.Contains(string(calls), "--output-schema") || strings.Contains(string(calls), "--json-schema") {
				t.Fatalf("version drift must stop before reviewer dispatch: %q", calls)
			}
		})
	}
}

func TestCodexDoctorFailureIsUnverifiedAndNonblocking(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then echo 'not-json'; exit 1; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-doctor-fail"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"approve\",\"findings\":[]}"}}'
printf '%s\n' '{"type":"turn.completed"}'
`
	dir, _ := installAdapterCLI(t, "codex", script)
	prepared, err := (CodexAdapter{}).Prepare(context.Background(), approvedTestRequest(t, Request{
		Prompt: "review", SchemaJSON: `{"type":"object"}`, WorkingDir: dir,
	}), &HandleStore{Path: filepath.Join(dir, "handles.json")})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	res, err := prepared.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("doctor failure must not block dispatch: %v", err)
	}
	if res.Provenance.ResolvedModel != "" || res.Provenance.ModelState != ObsUnverified ||
		res.Provenance.ModelDiagnostic == "" {
		t.Fatalf("doctor failure must record empty/unverified diagnostic: %+v", res.Provenance)
	}
}

func TestCodexDoctorTimeoutIsUnverifiedAndNonblocking(t *testing.T) {
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then sleep 2; exit 0; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-doctor-timeout"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"approve\",\"findings\":[]}"}}'
printf '%s\n' '{"type":"turn.completed"}'
	`
	dir, _ := installAdapterCLI(t, "codex", script)
	previousTimeout := codexModelProbeTimeout
	codexModelProbeTimeout = 100 * time.Millisecond
	defer func() { codexModelProbeTimeout = previousTimeout }()
	prepared, err := (CodexAdapter{}).Prepare(context.Background(), approvedTestRequest(t, Request{
		Prompt: "review", SchemaJSON: `{"type":"object"}`, WorkingDir: dir,
	}), &HandleStore{Path: filepath.Join(dir, "handles.json")})
	if err != nil {
		t.Fatalf("doctor timeout must not fail Prepare: %v", err)
	}
	defer prepared.Close()
	res, err := prepared.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("doctor timeout must not block dispatch: %v", err)
	}
	if res.Provenance.ResolvedModel != "" || res.Provenance.ModelState != ObsUnverified ||
		!strings.Contains(res.Provenance.ModelDiagnostic, "timed out") {
		t.Fatalf("doctor timeout must record empty/unverified diagnostic: %+v", res.Provenance)
	}
}

func TestAdvisoryProbePassDoesNotMaskActualCommandFailure(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		adapter Adapter
	}{
		{
			name: "claude",
			script: `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema --resume --safe-mode --add-dir --tools --permission-mode --system-prompt'; exit 0; fi
echo 'unknown option --json-schema' >&2
exit 2
`,
			adapter: ClaudeAdapter{},
		},
		{
			name: "codex",
			script: `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then echo '{}'; exit 0; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema --ignore-user-config --ignore-rules --strict-config --sandbox'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
echo 'unknown option --output-schema' >&2
exit 2
`,
			adapter: CodexAdapter{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := installAdapterCLI(t, tc.name, tc.script)
			prepared, err := tc.adapter.Prepare(context.Background(), approvedTestRequest(t, Request{
				Prompt: "review", SchemaJSON: `{"type":"object"}`, WorkingDir: dir,
			}), &HandleStore{Path: filepath.Join(dir, "handles.json")})
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Close()
			res, err := prepared.Dispatch(context.Background())
			if err == nil || res == nil || !res.Started {
				t.Fatalf("actual command rejection must fail after child start: result=%+v err=%v", res, err)
			}
			if res.Provenance.CapabilityProbeState != ProbeObserved {
				t.Fatalf("fixture must prove probe-pass/runtime-fail split: %+v", res.Provenance)
			}
		})
	}
}

func TestMissingOrMalformedStructuredOutputFailsAfterStart(t *testing.T) {
	tests := []struct {
		name    string
		script  string
		adapter Adapter
	}{
		{
			name: "claude",
			script: `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema --resume'; exit 0; fi
echo '{"type":"result","subtype":"success","is_error":false,"session_id":"session-no-output","modelUsage":{"claude-default":{}}}'
`,
			adapter: ClaudeAdapter{},
		},
		{
			name: "codex",
			script: `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then echo '{}'; exit 0; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-bad-output"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"not-json"}}'
printf '%s\n' '{"type":"turn.completed"}'
`,
			adapter: CodexAdapter{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := installAdapterCLI(t, tc.name, tc.script)
			prepared, err := tc.adapter.Prepare(context.Background(), approvedTestRequest(t, Request{
				Prompt: "review", SchemaJSON: `{"type":"object"}`, WorkingDir: dir,
			}), &HandleStore{Path: filepath.Join(dir, "handles.json")})
			if err != nil {
				t.Fatal(err)
			}
			defer prepared.Close()
			res, err := prepared.Dispatch(context.Background())
			if err == nil || res == nil || !res.Started || !strings.Contains(err.Error(), "FAILED") {
				t.Fatalf("missing/malformed structured output must be post-start FAILED: result=%+v err=%v", res, err)
			}
		})
	}
}

func TestPreflightRequiresSchema(t *testing.T) {
	dir := t.TempDir()
	for _, a := range []Adapter{ClaudeAdapter{}, CodexAdapter{}} {
		if err := a.Preflight(Request{Prompt: "x"}); err == nil ||
			!strings.Contains(err.Error(), "SchemaJSON") {
			t.Fatalf("%s: missing schema must fail preflight: %v", a.Vendor(), err)
		}
		if err := a.Preflight(approvedTestRequest(t, Request{Prompt: "x", SchemaJSON: `{"type":"object"}`, WorkingDir: dir})); err != nil {
			t.Fatalf("%s: valid request must pass preflight: %v", a.Vendor(), err)
		}
	}
}

// DR-813 C2: each real adapter performs version/schema/resume preparation
// exactly once before the returned one-shot Dispatch. Dispatch itself must
// not re-probe the CLI or recreate the Codex schema file.
func TestPreparedInvocationDoesNotRepeatPreflight(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		make   func() Adapter
	}{
		{
			name: "codex",
			script: `#!/bin/sh
echo "$@" >> "$ACRELAY_TEST_LOG"
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"approve\",\"findings\":[]}"}}'
printf '%s\n' '{"type":"turn.completed"}'
`,
			make: func() Adapter { return CodexAdapter{} },
		},
		{
			name: "claude",
			script: `#!/bin/sh
echo "$@" >> "$ACRELAY_TEST_LOG"
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"11111111-2222-3333-4444-555555555555","structured_output":{"verdict":"approve","findings":[]},"modelUsage":{"claude-test":{}}}'
`,
			make: func() Adapter { return ClaudeAdapter{} },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, logPath := installAdapterCLI(t, tc.name, tc.script)
			handles := &HandleStore{Path: filepath.Join(dir, "handles.json")}
			prepared, err := tc.make().Prepare(context.Background(), approvedTestRequest(t, Request{
				Prompt: "review", SchemaJSON: `{"type":"object"}`, WorkingDir: dir,
			}), handles)
			if err != nil {
				t.Fatal(err)
			}
			if pc, ok := prepared.(*preparedCodex); ok {
				if _, err := os.Stat(pc.schemaPath); err != nil {
					t.Fatalf("Codex schema must exist after Prepare: %v", err)
				}
			}
			res, err := prepared.Dispatch(context.Background())
			if err != nil || res == nil || !res.Started {
				t.Fatalf("prepared dispatch failed: result=%+v err=%v", res, err)
			}
			var schemaPath string
			if pc, ok := prepared.(*preparedCodex); ok {
				schemaPath = pc.schemaPath
			}
			if err := prepared.Close(); err != nil {
				t.Fatal(err)
			}
			if schemaPath != "" {
				if _, err := os.Stat(schemaPath); !os.IsNotExist(err) {
					t.Fatalf("Codex prepared schema must be cleaned by Close: %v", err)
				}
			}
			calls, _ := os.ReadFile(logPath)
			if strings.Count(string(calls), "--version") != 1 {
				t.Fatalf("version probe repeated after Prepare: %q", calls)
			}
		})
	}
}

// R0-CX-N1: timeout validation and normalization.
func TestTimeoutsValidate(t *testing.T) {
	norm, err := Timeouts{}.Validate()
	if err != nil {
		t.Fatal(err)
	}
	if norm != DefaultTimeouts() {
		t.Fatal("zero values must normalize to defaults")
	}
	if _, err := (Timeouts{Grace: 10 * time.Minute, Idle: 5 * time.Minute}).Validate(); err == nil {
		t.Fatal("grace >= idle must fail")
	}
	if _, err := (Timeouts{Startup: -1}).Validate(); err == nil {
		t.Fatal("negative values must fail")
	}
	// startup >= hard-cap would let hard-cap fire first and misclassify a
	// no-output hang as UNKNOWN instead of FAILED(timeout:startup) (R0-F2).
	if _, err := (Timeouts{Startup: 40 * time.Minute, Idle: 5 * time.Minute, HardCap: 30 * time.Minute, Grace: 10 * time.Second}).Validate(); err == nil {
		t.Fatal("startup >= hard-cap must fail")
	}
}

// R0-CX-F9: rotation vendor mismatch, empty entries, permission gate.
func TestHandleStoreHardening(t *testing.T) {
	dir := t.TempDir()
	h := &HandleStore{Path: filepath.Join(dir, "handles.json")}
	profile := profileID(WorkingDirNeutral)
	workingDir := testHandleWorkingDir(t)
	if _, err := h.Register("", "x", profile, workingDir); err == nil {
		t.Fatal("empty vendor must be refused")
	}
	if _, err := h.Register("claude", "  ", profile, workingDir); err == nil {
		t.Fatal("blank handle must be refused")
	}
	if _, err := h.Register("claude", "11111111-2222-3333-4444-555555555555", "", workingDir); err == nil {
		t.Fatal("blank trust profile must be refused")
	}
	if _, err := h.Register("claude", "11111111-2222-3333-4444-555555555555", profile, "relative"); err == nil {
		t.Fatal("relative handle working directory must be refused")
	}
	ref, err := h.Register("claude", "11111111-2222-3333-4444-555555555555", profile, workingDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ref) != len("sref-")+32 {
		t.Fatalf("expected 128-bit ref, got %s", ref)
	}
	if _, err := h.Rotate(ref, "codex", "thread-1", profile); err == nil ||
		!strings.Contains(err.Error(), "vendor mismatch") {
		t.Fatalf("cross-vendor rotation must fail closed: %v", err)
	}
	// privacy exposure fails closed on load. The weakening is
	// platform-appropriate: recreating the file plainly leaves 0644 perms on
	// POSIX and an unprotected inherited DACL on Windows — both must fail.
	weak, err := os.ReadFile(h.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(h.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.Path, weak, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := h.Lookup(ref); err == nil ||
		!strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("non-private store must fail closed: %v", err)
	}
}

// R0-CX-F8: SIGTERM-ignoring grandchildren die at grace escalation.
func TestGroupKillGraceEscalation(t *testing.T) {
	marker := "1799" // unique sleep duration as process marker
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	cmd := newGroupCmd(ctx, 500*time.Millisecond, "bash", "-c",
		"trap '' TERM; sleep "+marker+" & sleep "+marker+" & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	time.Sleep(300 * time.Millisecond) // let children spawn
	<-done                             // ctx timeout → TERM (ignored) → grace → group SIGKILL
	time.Sleep(700 * time.Millisecond) // allow the AfterFunc SIGKILL to land
	out, _ := exec.Command("pgrep", "-f", "sleep "+marker).Output()
	if len(strings.TrimSpace(string(out))) != 0 {
		exec.Command("pkill", "-9", "-f", "sleep "+marker).Run()
		t.Fatalf("grandchildren survived grace escalation: %q", out)
	}
}

func TestWatchdogBufferSignalsOnCompleteJSONLEventsOnly(t *testing.T) {
	w := newWatchdogBuffer()
	// complete JSONL events signal activity; repeated writes must not block
	// (channel capacity is 1)
	for i := 0; i < 10; i++ {
		if _, err := w.Write([]byte("{\"type\":\"event\"}\n")); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	select {
	case <-w.activity:
	default:
		t.Fatal("expected pending activity signal after complete JSONL events")
	}
	// event split across writes signals only once the newline lands
	w2 := newWatchdogBuffer()
	w2.Write([]byte("{\"type\":"))
	select {
	case <-w2.activity:
		t.Fatal("partial line must not signal")
	default:
	}
	w2.Write([]byte("\"done\"}\n"))
	select {
	case <-w2.activity:
	default:
		t.Fatal("completed event must signal")
	}
}

func TestWatchdogBufferPartialTrickleNeverSignals(t *testing.T) {
	w := newWatchdogBuffer()
	// byte trickle without a newline, malformed lines, and non-object JSON
	// must never count as events (owner cross-check item 4)
	for _, chunk := range []string{"garbage ", "not json\n", "123\n", "{broken\n", "{\"open\":"} {
		w.Write([]byte(chunk))
	}
	select {
	case <-w.activity:
		t.Fatal("trickle/malformed input must not reset timers")
	default:
	}
}

func TestSuperviseStartupTimeout(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	tmo := Timeouts{Startup: 30 * time.Millisecond, Idle: time.Second, HardCap: time.Minute, Grace: 10 * time.Millisecond}
	done := make(chan struct{})
	go func() { superviseTimeouts(ctx, cancel, make(chan struct{}), tmo); close(done) }()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor never fired startup timeout")
	}
	if cause := context.Cause(ctx); cause != ErrStartupTimeout {
		t.Fatalf("cause = %v, want ErrStartupTimeout", cause)
	}
	<-done
}

func TestSuperviseIdleTimeoutAfterActivity(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	tmo := Timeouts{Startup: time.Second, Idle: 40 * time.Millisecond, HardCap: time.Minute, Grace: 10 * time.Millisecond}
	activity := make(chan struct{}, 1)
	activity <- struct{}{} // first output arrives promptly, then the stream goes silent
	done := make(chan struct{})
	go func() { superviseTimeouts(ctx, cancel, activity, tmo); close(done) }()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor never fired idle timeout")
	}
	if cause := context.Cause(ctx); cause != ErrIdleTimeout {
		t.Fatalf("cause = %v, want ErrIdleTimeout", cause)
	}
	<-done
}

func TestSuperviseSteadyActivityStaysAlive(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	tmo := Timeouts{Startup: 200 * time.Millisecond, Idle: 200 * time.Millisecond, HardCap: time.Minute, Grace: 10 * time.Millisecond}
	activity := make(chan struct{}, 1)
	go superviseTimeouts(ctx, cancel, activity, tmo)
	for i := 0; i < 6; i++ { // keep the stream chatty well past startup+idle windows
		select {
		case activity <- struct{}{}:
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("supervisor cancelled a live stream: cause=%v", context.Cause(ctx))
	}
	cancel(nil)
}

func TestHandleStoreConcurrentRegisterLosesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handles.json")
	const n = 12
	errs := make(chan error, n)
	workingDirs := make([]string, n)
	for i := range workingDirs {
		workingDirs[i] = testHandleWorkingDir(t)
	}
	for i := 0; i < n; i++ {
		go func(i int) {
			// separate HandleStore values → separate lock fds, like separate processes
			s := &HandleStore{Path: path}
			_, err := s.Register("codex", "thread-"+string(rune('a'+i)), profileID(WorkingDirNeutral), workingDirs[i])
			errs <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent register: %v", err)
		}
	}
	s := &HandleStore{Path: path}
	f, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != n { // without the exclusive lock, interleaved load→save drops entries
		t.Fatalf("lost updates: %d entries survived, want %d", len(f.Entries), n)
	}
	if err := platform.VerifyPrivateFile(path + ".lock"); err != nil {
		t.Fatalf("lock file must be private: %v", err)
	}
}

// Helper entry for the multi-process flock fixture (R0-F4): real child
// processes exercise the three mutators (Register/Rotate/Delete) under a
// shared barrier so their critical sections genuinely overlap. Gated by env
// so the normal suite run skips it.
func TestHandleStoreHelperProcessMutate(t *testing.T) {
	path := os.Getenv("ACRELAY_HELPER_PATH")
	if path == "" {
		t.Skip("helper process entry — driven by TestHandleStoreConcurrentProcessesLoseNothing")
	}
	// Set the in-lock delay hook from test code only (R2-F2): production
	// never reads this env var — the helper (a test binary) does.
	if ms, err := strconv.Atoi(os.Getenv("ACRELAY_TEST_LOCK_DELAY_MS")); err == nil && ms > 0 {
		afterLockAcquired = func() { time.Sleep(time.Duration(ms) * time.Millisecond) }
	}
	barrier := os.Getenv("ACRELAY_HELPER_BARRIER")
	for { // all children release together — real critical-section overlap
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	s := &HandleStore{Path: path}
	profile := profileID(WorkingDirNeutral)
	workingDir, err := os.MkdirTemp("", "acrelay-review-root-helper-")
	if err != nil {
		t.Fatalf("helper working directory: %v", err)
	}
	defer os.RemoveAll(workingDir)
	ref, err := s.Register("codex", os.Getenv("ACRELAY_HELPER_HANDLE"), profile, workingDir)
	if err != nil {
		t.Fatalf("helper register: %v", err)
	}
	switch os.Getenv("ACRELAY_HELPER_OP") {
	case "rotate":
		if _, err := s.Rotate(ref, "codex", os.Getenv("ACRELAY_HELPER_HANDLE")+"-rot", profile); err != nil {
			t.Fatalf("helper rotate: %v", err)
		}
	case "delete":
		if err := s.Delete(ref); err != nil {
			t.Fatalf("helper delete: %v", err)
		}
	}
}

func TestHandleStoreConcurrentProcessesLoseNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "handles.json")
	barrier := path + ".barrier"
	const n = 12
	// ops cycle register-only / register+rotate / register+delete so all
	// three mutators contend on the same file at once. Each child's expected
	// per-op outcome is recorded so we verify outcomes, not just a count.
	ops := []string{"", "rotate", "delete"}
	wantHandles := map[string]bool{} // handle values that MUST survive
	bannedHandles := map[string]bool{}
	cmds := make([]*exec.Cmd, n)
	outs := make([]*strings.Builder, n)
	for i := range cmds {
		op := ops[i%len(ops)]
		base := "thread-proc-" + string(rune('a'+i))
		switch op {
		case "": // register-only: original handle survives
			wantHandles[base] = true
		case "rotate": // rotate: new handle survives, original gone
			wantHandles[base+"-rot"] = true
			bannedHandles[base] = true
		case "delete": // delete: nothing survives for this child
			bannedHandles[base] = true
			bannedHandles[base+"-rot"] = true
		}
		cmd := exec.Command(os.Args[0], "-test.run=TestHandleStoreHelperProcessMutate$", "-test.v")
		cmd.Env = append(os.Environ(),
			"ACRELAY_HELPER_PATH="+path,
			"ACRELAY_HELPER_BARRIER="+barrier,
			"ACRELAY_HELPER_OP="+op,
			"ACRELAY_TEST_LOCK_DELAY_MS=4", // widen the in-lock critical section
			"ACRELAY_HELPER_HANDLE="+base)
		outs[i] = &strings.Builder{}
		cmd.Stdout, cmd.Stderr = outs[i], outs[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[i] = cmd
	}
	time.Sleep(150 * time.Millisecond) // let every child reach the barrier wait
	if err := os.WriteFile(barrier, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper process %d failed: %v\n%s", i, err, outs[i].String())
		}
	}
	s := &HandleStore{Path: path}
	f, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range f.Entries {
		got[e.Handle] = true
	}
	if len(got) != len(f.Entries) {
		t.Fatalf("duplicate handle values in store — a mutator clobbered another: %d entries, %d distinct handles", len(f.Entries), len(got))
	}
	for h := range wantHandles {
		if !got[h] { // a lost register/rotate update
			t.Fatalf("handle %q missing — a concurrent mutator lost this update", h)
		}
	}
	for h := range bannedHandles {
		if got[h] { // a lost rotate/delete update left stale state
			t.Fatalf("handle %q survived — a concurrent rotate/delete update was lost", h)
		}
	}
	if len(got) != len(wantHandles) {
		t.Fatalf("store has %d handles, want exactly %d", len(got), len(wantHandles))
	}
}

// FEAT-20260721-002 R0-CX-F1: a malformed native handle is rejected before
// any child start — never truncated, dropped, or replaced by a new session.
func TestNativeHandleFormatNegativeTable(t *testing.T) {
	validClaude := "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		vendor, handle, why string
	}{
		{"claude", "short", "below minimum length"},
		{"claude", strings.Repeat("a", 129), "above maximum length"},
		{"claude", "-6f9619ff-8b86-d011-b42d-00cf4fc964ff", "leading option prefix"},
		{"claude", "11111111-2222-3333-4444-55555555555\n", "control character"},
		{"claude", "11111111-2222-3333-4444-5555555555 5", "embedded whitespace"},
		{"claude", "native-session-fixture", "non-UUID claude session shape"},
		{"codex", "-leading-dash-handle", "leading option prefix"},
		{"codex", "bad handle with spaces", "embedded whitespace"},
		{"codex", "handle;rm -rf", "argv-unsafe characters"},
		{"codex", "h\x01andle-ctrl", "control character"},
	} {
		if err := ValidateNativeHandle(tc.vendor, tc.handle); err == nil {
			t.Errorf("%s handle (%s) must fail closed", tc.vendor, tc.why)
		}
	}
	if err := ValidateNativeHandle("claude", validClaude); err != nil {
		t.Fatalf("valid claude UUID rejected: %v", err)
	}
	if err := ValidateNativeHandle("codex", "thread_0190b2c4-ok"); err != nil {
		t.Fatalf("valid codex handle rejected: %v", err)
	}

	// The store rejects malformed handles at Register AND at Lookup, so a
	// tampered file can never reach vendor argv.
	dir := t.TempDir()
	h := &HandleStore{Path: filepath.Join(dir, "handles.json")}
	if _, err := h.Register("claude", "native-session-fixture", profileID(WorkingDirNeutral), testHandleWorkingDir(t)); err == nil {
		t.Fatal("malformed claude handle must be refused at Register")
	}
}

// FEAT-20260721-002 precedence table, exercised against a real child process:
// a signal-killed child is UNKNOWN; a clean exit without the terminal
// contract is FAILED + transport.missing-terminal; a vendor error envelope
// carrying the observed resume-not-found signature classifies the cause as
// resume-handle-invalid (inferred) while remaining a consuming FAILED.
func TestTerminationClassificationAgainstRealChildren(t *testing.T) {
	run := func(t *testing.T, name, script string, resume bool) (*Result, error) {
		t.Helper()
		dir, _ := installAdapterCLI(t, name, script)
		handles := &HandleStore{Path: filepath.Join(dir, "handles.json")}
		req := approvedTestRequest(t, Request{Prompt: "review", SchemaJSON: `{"type":"object"}`})
		if resume {
			ref, err := handles.Register(name, "11111111-2222-3333-4444-555555555555",
				req.TrustPolicy.ProfileID, testHandleWorkingDir(t))
			if err != nil {
				t.Fatal(err)
			}
			req.ResumeRef = ref
		}
		var a Adapter = ClaudeAdapter{}
		if name == "codex" {
			a = CodexAdapter{}
		}
		prepared, err := a.Prepare(context.Background(), req, handles)
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Close()
		return prepared.Dispatch(context.Background())
	}

	t.Run("claude signal kill is UNKNOWN", func(t *testing.T) {
		skipWithoutPOSIXSignalDeath(t)
		res, err := run(t, "claude", `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema --resume --safe-mode --add-dir --tools --permission-mode --system-prompt'; exit 0; fi
kill -KILL $$
`, false)
		if err == nil || !res.Termination.Ambiguous ||
			res.Termination.Cause == nil || res.Termination.Cause.Code != CauseTerminatedSignal ||
			res.Termination.Cause.Source != CauseSourceObserved {
			t.Fatalf("signal kill must be ambiguous UNKNOWN: err=%v termination=%+v", err, res.Termination)
		}
		if !strings.Contains(err.Error(), "UNKNOWN") {
			t.Fatalf("error must state UNKNOWN, no automatic retry: %v", err)
		}
	})

	t.Run("claude clean exit without terminal contract is FAILED missing-terminal", func(t *testing.T) {
		res, err := run(t, "claude", `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema --resume --safe-mode --add-dir --tools --permission-mode --system-prompt'; exit 0; fi
exit 0
`, false)
		if err == nil || res.Termination.Ambiguous ||
			res.Termination.Cause == nil || res.Termination.Cause.Code != CauseMissingTerminal {
			t.Fatalf("clean no-output exit must be FAILED missing-terminal (DR-811): err=%v termination=%+v", err, res.Termination)
		}
	})

	t.Run("codex signal kill is UNKNOWN", func(t *testing.T) {
		skipWithoutPOSIXSignalDeath(t)
		res, err := run(t, "codex", `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then echo '{"checks":{"config.load":{"status":"ok","details":{"model":"m","provider":"p"}}}}'; exit 0; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema --ignore-user-config --ignore-rules --strict-config --sandbox'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-fixture-1"}'
kill -KILL $$
`, false)
		if err == nil || !res.Termination.Ambiguous ||
			res.Termination.Cause == nil || res.Termination.Cause.Code != CauseTerminatedSignal {
			t.Fatalf("codex signal kill must be ambiguous UNKNOWN: err=%v termination=%+v", err, res.Termination)
		}
	})

	t.Run("codex clean exit without terminal turn is FAILED missing-terminal", func(t *testing.T) {
		res, err := run(t, "codex", `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then echo '{"checks":{"config.load":{"status":"ok","details":{"model":"m","provider":"p"}}}}'; exit 0; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema --ignore-user-config --ignore-rules --strict-config --sandbox'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-fixture-1"}'
exit 0
`, false)
		if err == nil || res.Termination.Ambiguous ||
			res.Termination.Cause == nil || res.Termination.Cause.Code != CauseMissingTerminal {
			t.Fatalf("codex clean no-terminal exit must be FAILED missing-terminal: err=%v termination=%+v", err, res.Termination)
		}
	})

	t.Run("claude resume-not-found envelope classifies resume-handle-invalid", func(t *testing.T) {
		res, err := run(t, "claude", `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema --resume --safe-mode --add-dir --tools --permission-mode --system-prompt'; exit 0; fi
printf '%s\n' '{"type":"result","subtype":"success","is_error":true,"result":"No conversation found with session ID","session_id":"11111111-2222-3333-4444-555555555555"}'
exit 1
`, true)
		if err == nil || res.Termination.Ambiguous || res.Termination.Cause == nil ||
			res.Termination.Cause.Code != CauseResumeHandleInvalid ||
			res.Termination.Cause.Source != CauseSourceInferred {
			t.Fatalf("resume-not-found must classify resume-handle-invalid (inferred, consuming FAILED): err=%v termination=%+v", err, res.Termination)
		}
	})
}

// R1-CX-F6: a corrupted persisted handle-v2 file is rejected at Lookup — the
// tampered handle never reaches vendor argv.
func TestCorruptedHandleFileFailsClosedAtLookup(t *testing.T) {
	dir := t.TempDir()
	h := &HandleStore{Path: filepath.Join(dir, "handles.json")}
	ref, err := h.Register("codex", "thread-fixture-1", profileID(WorkingDirNeutral), testHandleWorkingDir(t))
	if err != nil {
		t.Fatal(err)
	}
	// Tamper the stored native handle into an argv-unsafe value.
	raw, err := os.ReadFile(h.Path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), "thread-fixture-1", "-injected --flag", 1)
	if err := platform.WritePrivateFile(h.Path, []byte(tampered)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := h.Lookup(ref); err == nil {
		t.Fatal("corrupted native handle must fail closed at Lookup, never reach vendor argv")
	}
}

// R1-CX-F6: precedence conflict — a conclusive terminal marker followed by a
// signal must keep the terminal classification, not become UNKNOWN.
func TestTerminalMarkerWinsOverLaterSignal(t *testing.T) {
	dir, _ := installAdapterCLI(t, "codex", `#!/bin/sh
if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then echo '{"checks":{"config.load":{"status":"ok","details":{"model":"m","provider":"p"}}}}'; exit 0; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema --ignore-user-config --ignore-rules --strict-config --sandbox'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-fixture-1"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"verdict\":\"approve\",\"examined\":[],\"findings\":[],\"approval_requests\":[]}"}}'
printf '%s\n' '{"type":"turn.completed"}'
kill -KILL $$
`)
	handles := &HandleStore{Path: filepath.Join(dir, "handles.json")}
	req := approvedTestRequest(t, Request{Prompt: "review", SchemaJSON: `{"type":"object"}`})
	prepared, err := CodexAdapter{}.Prepare(context.Background(), req, handles)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	res, err := prepared.Dispatch(context.Background())
	// A completed terminal turn is authoritative: the post-marker signal must
	// not turn this into an ambiguous UNKNOWN.
	if err != nil || res.Termination.Ambiguous || res.Structured == nil {
		t.Fatalf("terminal turn must win over the later signal: err=%v termination=%+v structured=%v",
			err, res.Termination, res.Structured != nil)
	}
}

// R2-CX-F1: the marker-first precedence boundary, pinned with real children
// across the conflict cases the packet promised.
func TestTerminationPrecedenceConflictMatrix(t *testing.T) {
	dispatch := func(t *testing.T, name, script string) (*Result, error) {
		t.Helper()
		dir, _ := installAdapterCLI(t, name, script)
		handles := &HandleStore{Path: filepath.Join(dir, "handles.json")}
		req := approvedTestRequest(t, Request{Prompt: "review", SchemaJSON: `{"type":"object"}`})
		var a Adapter = ClaudeAdapter{}
		if name == "codex" {
			a = CodexAdapter{}
		}
		prepared, err := a.Prepare(context.Background(), req, handles)
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Close()
		return prepared.Dispatch(context.Background())
	}
	codexHead := `if [ "$1" = "--version" ]; then echo "codex-cli 0.144.1"; exit 0; fi
if [ "$1" = "doctor" ]; then echo '{"checks":{"config.load":{"status":"ok","details":{"model":"m","provider":"p"}}}}'; exit 0; fi
if [ "$2" = "--help" ]; then echo '--json --output-schema --ignore-user-config --ignore-rules --strict-config --sandbox'; exit 0; fi
if [ "$3" = "--help" ]; then echo 'resume'; exit 0; fi
printf '%s\n' '{"type":"thread.started","thread_id":"thread-fixture-1"}'
`
	claudeHead := `if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema --resume --safe-mode --add-dir --tools --permission-mode --system-prompt'; exit 0; fi
`

	// 1. partial malformed JSONL + external signal → UNKNOWN (no terminal marker).
	t.Run("codex partial malformed then signal is UNKNOWN", func(t *testing.T) {
		skipWithoutPOSIXSignalDeath(t)
		res, err := dispatch(t, "codex", "#!/bin/sh\n"+codexHead+
			"printf '%s' '{\"type\":\"item.par'\nkill -KILL $$\n")
		if err == nil || !res.Termination.Ambiguous || res.Termination.Cause == nil ||
			res.Termination.Cause.Code != CauseTerminatedSignal {
			t.Fatalf("partial malformed + signal must be UNKNOWN(terminated.signal): err=%v termination=%+v", err, res.Termination)
		}
	})

	// 2. terminal failure event + later signal → vendor FAILED (marker wins).
	t.Run("codex turn.failed then signal stays vendor FAILED", func(t *testing.T) {
		res, err := dispatch(t, "codex", "#!/bin/sh\n"+codexHead+
			"printf '%s\\n' '{\"type\":\"turn.failed\",\"error\":{\"message\":\"boom\"}}'\nkill -KILL $$\n")
		if err == nil || res.Termination.Ambiguous || res.Termination.Cause == nil ||
			res.Termination.Cause.Code != CauseVendorTurnFailed || res.Termination.Cause.Source != CauseSourceVendorDeclared {
			t.Fatalf("terminal failure + signal must stay vendor FAILED: err=%v termination=%+v", err, res.Termination)
		}
	})

	// 3. no terminal marker + hard-cap → UNKNOWN.
	t.Run("codex no terminal marker then hard-cap is UNKNOWN", func(t *testing.T) {
		// Keep emitting non-terminal activity so the idle timer never fires;
		// only the hard-cap can end this, and with no terminal marker that is
		// an UNKNOWN.
		dir, _ := installAdapterCLI(t, "codex", "#!/bin/sh\n"+codexHead+
			"while true; do printf '%s\\n' '{\"type\":\"item.started\"}'; sleep 0.1; done\n")
		handles := &HandleStore{Path: filepath.Join(dir, "handles.json")}
		req := approvedTestRequest(t, Request{Prompt: "review", SchemaJSON: `{"type":"object"}`,
			Timeouts: Timeouts{Startup: 300 * time.Millisecond, Idle: 400 * time.Millisecond, HardCap: 600 * time.Millisecond, Grace: 100 * time.Millisecond}})
		prepared, err := CodexAdapter{}.Prepare(context.Background(), req, handles)
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Close()
		res, err := prepared.Dispatch(context.Background())
		if err == nil || !res.Termination.Ambiguous || res.Termination.Cause == nil ||
			res.Termination.Cause.Code != CauseTimeoutHardCap {
			t.Fatalf("no terminal marker + hard-cap must be UNKNOWN(hard-cap): err=%v termination=%+v", err, res.Termination)
		}
	})

	// 4. Claude terminal success + later signal → success wins (marker-first).
	t.Run("claude terminal success then signal keeps success", func(t *testing.T) {
		res, err := dispatch(t, "claude", "#!/bin/sh\n"+claudeHead+
			"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"session_id\":\"11111111-2222-3333-4444-555555555555\",\"structured_output\":{\"verdict\":\"approve\",\"findings\":[]},\"modelUsage\":{\"m\":{}}}'\nkill -KILL $$\n")
		if err != nil || res.Termination.Ambiguous || res.Structured == nil {
			t.Fatalf("claude terminal success must win over the later signal: err=%v termination=%+v", err, res.Termination)
		}
	})

	// 5. Claude no terminal envelope + signal → UNKNOWN.
	t.Run("claude signal without envelope is UNKNOWN", func(t *testing.T) {
		skipWithoutPOSIXSignalDeath(t)
		res, err := dispatch(t, "claude", "#!/bin/sh\n"+claudeHead+"kill -KILL $$\n")
		if err == nil || !res.Termination.Ambiguous || res.Termination.Cause == nil ||
			res.Termination.Cause.Code != CauseTerminatedSignal {
			t.Fatalf("claude signal without envelope must be UNKNOWN: err=%v termination=%+v", err, res.Termination)
		}
	})
}
