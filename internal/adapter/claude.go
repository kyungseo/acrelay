package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func (a ClaudeAdapter) Preflight(req Request) error {
	// Explicit effort must be validated here: the CLI silently ignores
	// unknown values (observed 2026-07-18), so validation is never delegated.
	return ValidateEffort(a.Capability(), req.Effort)
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
	diag := fmt.Sprintf("non-JSON prefix %d bytes before envelope", i)
	return &env, diag, nil
}

func (a ClaudeAdapter) Dispatch(ctx context.Context, req Request, handles *HandleStore) (*Result, error) {
	if err := a.Preflight(req); err != nil {
		return nil, err
	}
	args := []string{"-p", "--output-format", "json"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if req.SchemaJSON != "" {
		args = append(args, "--json-schema", req.SchemaJSON)
	}
	var resumeHandle string
	if req.ResumeRef != "" {
		vendor, h, err := handles.Lookup(req.ResumeRef)
		if err != nil {
			return nil, err // fail-closed: no silent new-session fallback
		}
		if vendor != "claude" {
			return nil, fmt.Errorf("session_ref %s belongs to %s, not claude: fail-closed", req.ResumeRef, vendor)
		}
		resumeHandle = h
		args = append(args, "--resume", resumeHandle)
	}

	tctx, cancel := context.WithTimeout(ctx, req.Timeouts.HardCap)
	defer cancel()
	cmd := newGroupCmd(tctx, "claude", args...)
	cmd.Dir = req.WorkingDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Stdin = bytes.NewReader([]byte(req.Prompt))
	runErr := cmd.Run()

	res := &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: cmd.ProcessState.ExitCode()}
	if tctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		return res, fmt.Errorf("hard-cap timeout: execution UNKNOWN, re-dispatch forbidden")
	}
	env, diag, perr := parseClaudeEnvelope(stdout.Bytes())
	if perr != nil {
		return res, fmt.Errorf("dispatch capture failed (runErr=%v): %w", runErr, perr)
	}
	_ = diag // recorded by the caller alongside raw bytes
	if env.IsError {
		return res, fmt.Errorf("claude reported error in envelope (exit=%d): FAILED", res.ExitCode)
	}
	res.Structured = env.Structured

	sessionRef := req.ResumeRef
	newSession := false
	if sessionRef == "" {
		ref, err := handles.Register("claude", env.SessionID)
		if err != nil {
			return res, err
		}
		sessionRef, newSession = ref, true
	}
	resolved, modelState := "", ObsUnverified
	for k := range env.ModelUsage {
		resolved, modelState = k, ObsVerified
	}
	effortState := ObsUnsupported
	if req.Effort != "" {
		effortState = ObsAttested // accepted pre-validated flag; no echo on success path
	}
	res.Provenance = Provenance{
		RequestedModel: req.Model, ResolvedModel: resolved, ModelState: modelState,
		RequestedEffort: req.Effort, EffortState: effortState,
		CLIVersion: a.Capability().CLIVersionChecked, WorkingDir: req.WorkingDir,
		SessionRef: sessionRef, NewSession: newSession,
	}
	return res, nil
}
