package adapter

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestHandleStoreLifecycle(t *testing.T) {
	dir := t.TempDir()
	h := &HandleStore{Path: filepath.Join(dir, "handles.json")}

	ref1, err := h.Register("claude", "native-uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ref1, "native-uuid-1") {
		t.Fatal("session_ref must not embed the native handle")
	}
	// 0600 from creation
	st, _ := os.Stat(h.Path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("handle store mode %o, want 0600", st.Mode().Perm())
	}
	// merge preserved
	ref2, _ := h.Register("codex", "thread-2")
	if _, nh, err := h.Lookup(ref1); err != nil || nh != "native-uuid-1" {
		t.Fatalf("first entry lost after second register: %v", err)
	}
	// randomness: two registrations of the same handle produce distinct refs
	ref3, _ := h.Register("claude", "native-uuid-1")
	if ref1 == ref3 {
		t.Fatal("references must be random, not derived from the handle")
	}
	// rotation: old ref removed, new ref resolves
	ref4, err := h.Rotate(ref2, "codex", "thread-2b")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Lookup(ref2); err == nil {
		t.Fatal("rotated-away ref must fail closed")
	}
	if _, nh, _ := h.Lookup(ref4); nh != "thread-2b" {
		t.Fatal("rotated ref must resolve to the new handle")
	}
	// deletion
	if err := h.Delete(ref4); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Lookup(ref4); err == nil {
		t.Fatal("deleted ref must fail closed")
	}
	// missing ref fails closed (no silent new-session fallback)
	if _, _, err := h.Lookup("sref-doesnotexist"); err == nil ||
		!strings.Contains(err.Error(), "no silent new-session fallback") {
		t.Fatalf("missing ref must fail closed with explicit contract: %v", err)
	}
}

func TestHandleStoreCorruptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	h := &HandleStore{Path: filepath.Join(dir, "handles.json")}
	if err := os.WriteFile(h.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Register("claude", "x"); err == nil {
		t.Fatal("corrupt store must fail closed, never be overwritten")
	}
	if b, _ := os.ReadFile(h.Path); string(b) != "{not json" {
		t.Fatal("corrupt store content must remain untouched")
	}
	// unsupported version fails closed
	os.WriteFile(h.Path, []byte(`{"version":99,"entries":{}}`), 0o600)
	if _, _, err := (&HandleStore{Path: h.Path}).Lookup("any"); err == nil ||
		!strings.Contains(err.Error(), "version") {
		t.Fatalf("version mismatch must fail closed: %v", err)
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

// R0-CX-F6: version gate and mandatory schema.
func TestPreflightVersionGate(t *testing.T) {
	cap := ClaudeAdapter{}.Capability()
	if err := PreflightVersion(cap, "2.1.214"); err != nil {
		t.Fatal(err)
	}
	if err := PreflightVersion(cap, "9.9.9"); err == nil {
		t.Fatal("unvalidated CLI version must fail closed")
	}
	if err := PreflightVersion(cap, ""); err == nil {
		t.Fatal("unobservable version must fail closed")
	}
}

func TestPreflightRequiresSchema(t *testing.T) {
	for _, a := range []Adapter{ClaudeAdapter{}, CodexAdapter{}} {
		if err := a.Preflight(Request{Prompt: "x"}); err == nil ||
			!strings.Contains(err.Error(), "SchemaJSON") {
			t.Fatalf("%s: missing schema must fail preflight: %v", a.Vendor(), err)
		}
		if err := a.Preflight(Request{Prompt: "x", SchemaJSON: `{"type":"object"}`}); err != nil {
			t.Fatalf("%s: valid request must pass preflight: %v", a.Vendor(), err)
		}
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
}

// R0-CX-F9: rotation vendor mismatch, empty entries, permission gate.
func TestHandleStoreHardening(t *testing.T) {
	dir := t.TempDir()
	h := &HandleStore{Path: filepath.Join(dir, "handles.json")}
	if _, err := h.Register("", "x"); err == nil {
		t.Fatal("empty vendor must be refused")
	}
	if _, err := h.Register("claude", "  "); err == nil {
		t.Fatal("blank handle must be refused")
	}
	ref, err := h.Register("claude", "native-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ref) != len("sref-")+32 {
		t.Fatalf("expected 128-bit ref, got %s", ref)
	}
	if _, err := h.Rotate(ref, "codex", "thread-1"); err == nil ||
		!strings.Contains(err.Error(), "vendor mismatch") {
		t.Fatalf("cross-vendor rotation must fail closed: %v", err)
	}
	// permission exposure fails closed on load
	os.Chmod(h.Path, 0o644)
	if _, _, err := h.Lookup(ref); err == nil ||
		!strings.Contains(err.Error(), "permission") {
		t.Fatalf("group/other-readable store must fail closed: %v", err)
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
