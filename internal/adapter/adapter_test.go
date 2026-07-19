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
	for i := 0; i < n; i++ {
		go func(i int) {
			// separate HandleStore values → separate lock fds, like separate processes
			s := &HandleStore{Path: path}
			_, err := s.Register("codex", "thread-"+string(rune('a'+i)))
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
	st, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("lock file: %v", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("lock file permission %o exposes group/other", st.Mode().Perm())
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
	barrier := os.Getenv("ACRELAY_HELPER_BARRIER")
	for { // all children release together — real critical-section overlap
		if _, err := os.Stat(barrier); err == nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	s := &HandleStore{Path: path}
	ref, err := s.Register("codex", os.Getenv("ACRELAY_HELPER_HANDLE"))
	if err != nil {
		t.Fatalf("helper register: %v", err)
	}
	switch os.Getenv("ACRELAY_HELPER_OP") {
	case "rotate":
		if _, err := s.Rotate(ref, "codex", os.Getenv("ACRELAY_HELPER_HANDLE")+"-rot"); err != nil {
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
