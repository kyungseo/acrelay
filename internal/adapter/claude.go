package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ClaudeAdapter binds the Claude Code CLI (`claude -p`) in final-envelope
// mode. Idle timeout is unsupported in this mode and declared so.
type ClaudeAdapter struct{}

func (ClaudeAdapter) Vendor() string { return "claude" }

func (ClaudeAdapter) Capability() Capability {
	return Capability{
		Vendor:            "claude",
		CLIVersionChecked: "2.1.214",
		EffortEnum:        []string{"low", "medium", "high", "xhigh", "max"},
		SchemaFlag:        "--json-schema",
		SupportsResume:    true,
		ModelObservation:  ObsVerified, // resolved model observable via modelUsage
		ProgressEvents:    true,        // stream-json surface exists; v1 dispatch uses final envelope
		IdleTimeoutMode:   "unsupported",
	}
}

// Preflight validates effort (the CLI silently ignores unknown values —
// observed 2026-07-18 — so validation is never delegated), mandatory
// schema, and timeout structure.
func (a ClaudeAdapter) Preflight(req Request) error {
	return validateCommonRequest(a.Capability(), req)
}

// claudeEnvelope is the subset of the final JSON envelope the relay reads.
type claudeEnvelope struct {
	Type       string                     `json:"type"`
	Subtype    string                     `json:"subtype"`
	IsError    bool                       `json:"is_error"`
	Result     string                     `json:"result"`
	SessionID  string                     `json:"session_id"`
	Structured map[string]any             `json:"structured_output"`
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
}

// parseClaudeEnvelope parses stdout. A non-JSON prefix (e.g. CLI warnings)
// is surfaced as an explicit diagnostic, never silently skipped; the raw
// bytes stay intact for the canonical record either way.
func parseClaudeEnvelope(stdout []byte) (*claudeEnvelope, string, error) {
	var env claudeEnvelope
	if err := json.Unmarshal(stdout, &env); err == nil {
		return &env, "", nil
	}
	i := bytes.IndexByte(stdout, '{')
	if i < 0 {
		return nil, "", fmt.Errorf("claude stdout contains no JSON envelope: fail-closed")
	}
	if err := json.Unmarshal(stdout[i:], &env); err != nil {
		return nil, "", fmt.Errorf("claude envelope malformed after %d-byte prefix: %w", i, err)
	}
	return &env, fmt.Sprintf("non-JSON prefix %d bytes before envelope", i), nil
}

// detectClaudeVersion parses `claude --version` ("2.1.214 (Claude Code)").
func detectClaudeVersion(ctx context.Context) (string, error) {
	banner, err := probeVersion(ctx, "claude", "--version")
	if err != nil {
		return "", err
	}
	return firstNonEmptyLineField(banner, 0), nil
}

func (a ClaudeAdapter) Dispatch(ctx context.Context, req Request, handles *HandleStore) (*Result, error) {
	if err := a.Preflight(req); err != nil {
		return nil, err
	}
	timeouts, err := req.Timeouts.Validate()
	if err != nil {
		return nil, err
	}
	observedVersion, err := detectClaudeVersion(ctx)
	if err != nil {
		return nil, err
	}
	if err := PreflightVersion(a.Capability(), observedVersion); err != nil {
		return nil, err
	}

	args := []string{"-p", "--output-format", "json", "--json-schema", req.SchemaJSON}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if req.ResumeRef != "" {
		vendor, h, err := handles.Lookup(req.ResumeRef)
		if err != nil {
			return nil, err // fail-closed: no silent new-session fallback
		}
		if vendor != "claude" {
			return nil, fmt.Errorf("session_ref %s belongs to %s, not claude: fail-closed", req.ResumeRef, vendor)
		}
		args = append(args, "--resume", h)
	}

	tctx, cancel := context.WithTimeout(ctx, timeouts.HardCap)
	defer cancel()
	cmd := newGroupCmd(tctx, timeouts.Grace, "claude", args...)
	cmd.Dir = req.WorkingDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Stdin = bytes.NewReader([]byte(req.Prompt))
	runErr := cmd.Run()

	res := &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode(cmd)}
	if cmd.ProcessState == nil {
		return res, fmt.Errorf("claude process never started (pre-dispatch failure, no attempt consumed): %v", runErr)
	}
	if tctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		return res, fmt.Errorf("hard-cap timeout: execution UNKNOWN, re-dispatch forbidden")
	}
	env, diag, perr := parseClaudeEnvelope(stdout.Bytes())
	if perr != nil {
		return res, fmt.Errorf("dispatch capture failed (runErr=%v): %w", runErr, perr)
	}
	res.Diagnostic = diag
	if env.IsError {
		return res, fmt.Errorf("claude reported error in envelope (exit=%d): FAILED", res.ExitCode)
	}
	if strings.TrimSpace(env.SessionID) == "" {
		return res, fmt.Errorf("claude envelope has no session_id: fail-closed (empty handles are never stored)")
	}
	res.Structured = env.Structured
	if env.Structured == nil {
		res.Invalid = append(res.Invalid, "missing-structured-output")
	}

	sessionRef := req.ResumeRef
	newSession := false
	if sessionRef == "" {
		ref, err := handles.Register("claude", env.SessionID)
		if err != nil {
			return res, err
		}
		sessionRef, newSession = ref, true
	}
	resolved, modelState := "", ObservationState(ObsUnverified)
	for k := range env.ModelUsage {
		resolved, modelState = k, ObsVerified
	}
	mismatch := req.Model != "" && resolved != "" && !strings.Contains(resolved, req.Model)
	effortState := ObservationState("")
	if req.Effort != "" {
		effortState = ObsAttested // accepted pre-validated flag; no echo on success path
	}
	res.Provenance = Provenance{
		RequestedModel: req.Model, ResolvedModel: resolved, ModelState: modelState, ModelMismatch: mismatch,
		RequestedEffort: req.Effort, EffortState: effortState,
		ManifestCLIVersion: a.Capability().CLIVersionChecked, ObservedCLIVersion: observedVersion,
		WorkingDir: req.WorkingDir, SessionRef: sessionRef, NewSession: newSession,
	}
	return res, nil
}
