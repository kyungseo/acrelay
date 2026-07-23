package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ClaudeAdapter binds the Claude Code CLI (`claude -p`) in final-envelope
// mode. There is no observable output before the terminal envelope, so both
// startup and idle timers are unsupported in this mode and declared so —
// only the hard-cap bounds the invocation (DR-811 §7, amended 2026-07-18:
// the final-envelope exception covers startup as well as idle).
type ClaudeAdapter struct{}

func (ClaudeAdapter) Vendor() string { return "claude" }

func (ClaudeAdapter) Capability() Capability {
	return Capability{
		Vendor:              "claude",
		ContractVersion:     "claude-final-envelope-v1",
		KnownGoodCLIVersion: "2.1.217",
		// 2026-07-22 restriction spike ran on darwin/arm64 (FEAT-20260722-002).
		KnownGoodPlatforms: []string{"darwin/arm64"},
		EffortEnum:         []string{"low", "medium", "high", "xhigh", "max"},
		SchemaFlag:         "--json-schema",
		SupportsResume:     true,
		ModelObservation:   ObsVerified, // resolved model observable via modelUsage
		ProgressEvents:     true,        // stream-json surface exists; v1 dispatch uses final envelope
		IdleTimeoutMode:    "unsupported",
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

// detectClaudeVersion parses `claude --version` ("2.1.217 (Claude Code)")
// while retaining the normalized banner for provenance.
func detectClaudeVersion(ctx context.Context) (string, string, error) {
	banner, err := probeVersion(ctx, "claude", "--version")
	if err != nil {
		return "", "", err
	}
	return firstNonEmptyLineField(banner, 0), strings.TrimSpace(banner), nil
}

func probeClaudeCapabilities(ctx context.Context) (string, string) {
	out, truncated, err := runBoundedProbe(ctx, 15*time.Second, "claude", "--help")
	return assessHelpProbe(out, truncated, err, "--output-format", "--json-schema", "--resume", "--safe-mode", "--add-dir", "--tools", "--permission-mode", "--system-prompt")
}

type preparedClaude struct {
	req        Request
	handles    *HandleStore
	args       []string
	timeouts   Timeouts
	provenance Provenance
	cleanupDir string
}

func (p *preparedClaude) Close() error {
	if p.cleanupDir == "" {
		return nil
	}
	err := os.RemoveAll(p.cleanupDir)
	p.cleanupDir = ""
	return err
}

// Prepare completes every fallible operation before the relay creates the
// dispatch journal. Dispatch therefore starts the already-decided command;
// it never repeats version/resume/schema preparation.
func (a ClaudeAdapter) Prepare(ctx context.Context, req Request, handles *HandleStore) (PreparedInvocation, error) {
	if err := a.Preflight(req); err != nil {
		return nil, err
	}
	timeouts, err := req.Timeouts.Validate()
	if err != nil {
		return nil, err
	}
	observed, banner, err := detectClaudeVersion(ctx)
	if err != nil {
		return nil, err
	}
	if err := VerifyRestrictionEvidence(a.Capability(), observed); err != nil {
		return nil, err
	}
	probeState, probeDiagnostic := probeClaudeCapabilities(ctx)
	resumeHandle := ""
	resumeWorkingDir := ""
	if req.ResumeRef != "" {
		vendor, h, profileID, workingDir, err := handles.Lookup(req.ResumeRef)
		if err != nil {
			return nil, err
		}
		if vendor != "claude" {
			return nil, fmt.Errorf("session_ref %s belongs to %s, not claude: fail-closed", req.ResumeRef, vendor)
		}
		if profileID != req.TrustPolicy.ProfileID {
			return nil, fmt.Errorf("session_ref %s trust profile mismatch (%s != %s): explicit session reset required, fail-closed",
				req.ResumeRef, profileID, req.TrustPolicy.ProfileID)
		}
		resumeHandle = h
		resumeWorkingDir = workingDir
	}
	preparedReq, cleanupDir, err := prepareExecutionRoot(req, resumeWorkingDir, handles)
	if err != nil {
		return nil, err
	}
	req = preparedReq
	args := []string{
		"-p",
		"--safe-mode",
		"--add-dir", req.SubjectRoot,
		"--no-chrome",
		"--disable-slash-commands",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
		"--tools", "Read,Glob,Grep",
		"--permission-mode", "dontAsk",
		"--system-prompt", ReviewerTrustSystemPrompt,
		"--output-format", "json",
		"--json-schema", req.SchemaJSON,
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if resumeHandle != "" {
		args = append(args, "--resume", resumeHandle)
	}
	prov := baseProvenance(a.Capability(), req, observed, banner, "claude --version",
		probeState, probeDiagnostic)
	prov.ModelState = ObsUnverified
	prov.ModelSource = "claude terminal envelope:modelUsage"
	return &preparedClaude{req: req, handles: handles, args: args, timeouts: timeouts, provenance: prov, cleanupDir: cleanupDir}, nil
}

func (p *preparedClaude) Dispatch(ctx context.Context) (*Result, error) {
	req, handles := p.req, p.handles
	tctx, cancel := context.WithTimeout(ctx, p.timeouts.HardCap)
	defer cancel()
	cmd := newGroupCmd(tctx, p.timeouts.Grace, "claude", p.args...)
	cmd.Dir = req.WorkingDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Stdin = bytes.NewReader([]byte(req.Prompt))
	runErr := runWithProgress(cmd, req.Progress)

	res := &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode(cmd),
		Started: startedForResult(cmd, runErr), Provenance: p.provenance}
	if !res.Started {
		return res, fmt.Errorf("claude process never started (pre-dispatch failure, no attempt consumed): %v", runErr)
	}
	// FEAT-20260721-002 R1-CX-F1: a conclusive terminal marker takes
	// precedence over every timeout/cancellation/signal classification — a
	// completed vendor result is never discarded because the invocation was
	// cut afterwards. Only when no terminal envelope exists do the
	// ambiguity/transport rows apply.
	env, diag, perr := parseClaudeEnvelope(stdout.Bytes())
	if perr != nil || env.Type != "result" {
		switch {
		case errors.Is(context.Cause(tctx), ErrParentSignal):
			res.Termination = Termination{Ambiguous: true, Cause: observedCause(CauseCanceledParentSignal)}
			return res, fmt.Errorf("parent signal canceled the dispatch before terminal output: execution UNKNOWN, no automatic retry")
		case tctx.Err() == context.DeadlineExceeded:
			res.TimedOut, res.TimeoutKind = true, TimeoutHardCap
			res.Termination = Termination{Ambiguous: true, Cause: observedCause(CauseTimeoutHardCap)}
			return res, fmt.Errorf("hard-cap timeout: execution UNKNOWN, re-dispatch forbidden")
		case terminatedBySignal(cmd):
			res.Termination = Termination{Ambiguous: true, Cause: observedCause(CauseTerminatedSignal)}
			return res, fmt.Errorf("claude process terminated by signal before terminal output: execution UNKNOWN, no automatic retry")
		case perr != nil && len(bytes.TrimSpace(stdout.Bytes())) == 0:
			res.Termination.Cause = observedCause(CauseMissingTerminal)
			return res, fmt.Errorf("dispatch capture failed (runErr=%v): FAILED, no automatic retry: %w", runErr, perr)
		case perr != nil:
			res.Termination.Cause = observedCause(CauseMalformedTerminal)
			return res, fmt.Errorf("dispatch capture failed (runErr=%v): FAILED, no automatic retry: %w", runErr, perr)
		default: // parsed JSON without the terminal result type
			res.Termination.Cause = observedCause(CauseMalformedTerminal)
			return res, fmt.Errorf("claude output is not a terminal result envelope (type=%s): fail-closed", env.Type)
		}
	}
	res.Diagnostic = joinDiagnostics(p.provenance.CapabilityProbeDiagnostic, diag)
	if env.Subtype != "success" {
		res.Termination.Cause = &FailureCause{Code: CauseVendorErrorEnvelope, Source: CauseSourceVendorDeclared}
		return res, fmt.Errorf("claude envelope is not a terminal success (type=%s subtype=%s): fail-closed", env.Type, env.Subtype)
	}
	if env.IsError {
		// Cause inference from vendor text is never a verified fact. The
		// resume-not-found signature was observed on Claude 2.1.215
		// (FEAT-20260720-002); on drift this falls back to the declared
		// error-envelope cause.
		cause := &FailureCause{Code: CauseVendorErrorEnvelope, Source: CauseSourceVendorDeclared}
		if req.ResumeRef != "" && strings.Contains(env.Result, "No conversation found") {
			cause = &FailureCause{Code: CauseResumeHandleInvalid, Source: CauseSourceInferred}
		}
		res.Termination.Cause = cause
		return res, fmt.Errorf("claude reported error in envelope (exit=%d): FAILED", res.ExitCode)
	}
	if strings.TrimSpace(env.SessionID) == "" {
		res.Termination.Cause = observedCause(CauseMalformedTerminal)
		return res, fmt.Errorf("claude envelope has no session_id: fail-closed (empty handles are never stored)")
	}
	res.Structured = env.Structured
	if env.Structured == nil {
		res.Termination.Cause = observedCause(CauseNoStructuredOutput)
		return res, fmt.Errorf("claude terminal envelope has no structured_output: FAILED, no automatic retry")
	}

	sessionRef := req.ResumeRef
	newSession := false
	if sessionRef == "" {
		ref, err := handles.Register("claude", env.SessionID, req.TrustPolicy.ProfileID, req.WorkingDir)
		if err != nil {
			return res, err
		}
		// The vendor binds resume lookup to cwd. Transfer the fresh neutral
		// directory to the private handle lifecycle after registration.
		p.cleanupDir = ""
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
	res.Provenance.ResolvedModel = resolved
	res.Provenance.ModelState = modelState
	res.Provenance.ModelSource = "claude terminal envelope:modelUsage"
	res.Provenance.ModelMismatch = mismatch
	res.Provenance.RequestedEffort = req.Effort
	res.Provenance.EffortState = effortState
	res.Provenance.SessionRef = sessionRef
	res.Provenance.NewSession = newSession
	return res, nil
}
