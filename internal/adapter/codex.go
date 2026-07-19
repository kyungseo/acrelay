package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// CodexAdapter binds the Codex CLI (`codex exec --json`). Resolved model and
// effort are not echoed on the public structured stdout, so their
// observation state never exceeds attested.
type CodexAdapter struct{}

func (CodexAdapter) Vendor() string { return "codex" }

func (CodexAdapter) Capability() Capability {
	return Capability{
		Vendor:            "codex",
		CLIVersionChecked: "0.144.1",
		EffortEnum:        []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		SchemaFlag:        "--output-schema",
		SupportsResume:    true, // codex exec resume <thread_id>
		ModelObservation:  ObsAttested,
		ProgressEvents:    true, // JSONL event stream
		IdleTimeoutMode:   "event-stream",
	}
}

func (a CodexAdapter) Preflight(req Request) error {
	return validateCommonRequest(a.Capability(), req)
}

// codexCapture is the parse result of one JSONL stream.
type codexCapture struct {
	ThreadID      string
	AgentMessage  string
	TurnCompleted bool
	TurnFailed    bool
	FailReason    string
}

// parseCodexJSONL parses the event stream. Malformed event lines fail
// closed with an explicit diagnostic — they are never skipped silently.
func parseCodexJSONL(stdout []byte) (*codexCapture, error) {
	capd := &codexCapture{}
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var ev struct {
			Type   string `json:"type"`
			Thread string `json:"thread_id"`
			Item   struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("codex JSONL line %d malformed: fail-closed: %w", line, err)
		}
		switch ev.Type {
		case "thread.started":
			capd.ThreadID = ev.Thread
		case "item.completed":
			if ev.Item.Type == "agent_message" {
				capd.AgentMessage = ev.Item.Text
			}
		case "turn.completed":
			capd.TurnCompleted = true
		case "turn.failed":
			capd.TurnFailed = true
			capd.FailReason = ev.Error.Message
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("codex JSONL scan: %w", err)
	}
	return capd, nil
}

// detectCodexVersion parses `codex --version` ("codex-cli 0.144.1").
func detectCodexVersion(ctx context.Context) (string, error) {
	banner, err := probeVersion(ctx, "codex", "--version")
	if err != nil {
		return "", err
	}
	return firstNonEmptyLineField(banner, 1), nil
}

// PreDispatch runs preflight, the version gate, and resume-ref resolution
// without consuming any budget.
func (a CodexAdapter) PreDispatch(ctx context.Context, req Request, handles *HandleStore) error {
	if err := a.Preflight(req); err != nil {
		return err
	}
	observed, err := detectCodexVersion(ctx)
	if err != nil {
		return err
	}
	if err := PreflightVersion(a.Capability(), observed); err != nil {
		return err
	}
	if req.ResumeRef != "" {
		vendor, _, err := handles.Lookup(req.ResumeRef)
		if err != nil {
			return err
		}
		if vendor != "codex" {
			return fmt.Errorf("session_ref %s belongs to %s, not codex: fail-closed", req.ResumeRef, vendor)
		}
	}
	return nil
}

func (a CodexAdapter) Dispatch(ctx context.Context, req Request, handles *HandleStore) (*Result, error) {
	if err := a.Preflight(req); err != nil {
		return nil, err
	}
	timeouts, err := req.Timeouts.Validate()
	if err != nil {
		return nil, err
	}
	observedVersion, err := detectCodexVersion(ctx)
	if err != nil {
		return nil, err
	}
	if err := PreflightVersion(a.Capability(), observedVersion); err != nil {
		return nil, err
	}

	args := []string{"exec"}
	if req.ResumeRef != "" {
		vendor, h, err := handles.Lookup(req.ResumeRef)
		if err != nil {
			return nil, err
		}
		if vendor != "codex" {
			return nil, fmt.Errorf("session_ref %s belongs to %s, not codex: fail-closed", req.ResumeRef, vendor)
		}
		args = append(args, "resume", h)
	}
	args = append(args, "--skip-git-repo-check", "--json")
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+req.Effort)
	}
	schemaFile, err := os.CreateTemp("", "acrelay-schema-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(schemaFile.Name())
	if _, err := schemaFile.WriteString(req.SchemaJSON); err != nil {
		schemaFile.Close()
		return nil, err
	}
	schemaFile.Close()
	args = append(args, "--output-schema", schemaFile.Name())
	// Prompt travels over stdin ("-" positional) so leading-dash content can
	// never be parsed as a flag.
	args = append(args, "-")

	hctx, hcancel := context.WithTimeout(ctx, timeouts.HardCap)
	defer hcancel()
	tctx, tcancel := context.WithCancelCause(hctx)
	defer tcancel(nil)
	cmd := newGroupCmd(tctx, timeouts.Grace, "codex", args...)
	cmd.Dir = req.WorkingDir
	stdout := newWatchdogBuffer()
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	cmd.Stdin = bytes.NewReader([]byte(req.Prompt))
	// Event-stream mode: startup and idle are observable-output timers wired
	// to the JSONL stream (DR-811 §7).
	go superviseTimeouts(tctx, tcancel, stdout.activity, timeouts)
	runErr := runWithProgress(cmd, req.Progress)

	res := &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode(cmd), Started: cmd.ProcessState != nil}
	if !res.Started {
		return res, fmt.Errorf("codex process never started (pre-dispatch failure, no attempt consumed): %v", runErr)
	}
	switch cause := context.Cause(tctx); {
	case errors.Is(cause, ErrStartupTimeout):
		res.TimedOut, res.TimeoutKind = true, TimeoutStartup
		return res, fmt.Errorf("startup timeout after %v: FAILED(timeout:startup), no automatic retry", timeouts.Startup)
	case errors.Is(cause, ErrIdleTimeout):
		res.TimedOut, res.TimeoutKind = true, TimeoutIdle
		return res, fmt.Errorf("idle timeout after %v of stream silence: FAILED(timeout:idle), no automatic retry", timeouts.Idle)
	case hctx.Err() == context.DeadlineExceeded:
		res.TimedOut, res.TimeoutKind = true, TimeoutHardCap
		return res, fmt.Errorf("hard-cap timeout: execution UNKNOWN, re-dispatch forbidden")
	}
	capd, perr := parseCodexJSONL(stdout.Bytes())
	if perr != nil {
		return res, fmt.Errorf("dispatch capture failed (runErr=%v): %w", runErr, perr)
	}
	if capd.TurnFailed || (runErr != nil && res.ExitCode != 0) {
		return res, fmt.Errorf("codex turn failed (exit=%d, reason=%.120s): FAILED", res.ExitCode, capd.FailReason)
	}
	if !capd.TurnCompleted {
		return res, fmt.Errorf("codex stream ended without a terminal turn event: fail-closed")
	}
	if strings.TrimSpace(capd.ThreadID) == "" {
		return res, fmt.Errorf("codex stream has no thread_id: fail-closed (empty handles are never stored)")
	}
	if capd.AgentMessage == "" {
		res.Invalid = append(res.Invalid, "missing-structured-output")
	} else {
		var m map[string]any
		if err := json.Unmarshal([]byte(capd.AgentMessage), &m); err != nil {
			res.Invalid = append(res.Invalid, "malformed-structured-output")
		} else {
			res.Structured = m
		}
	}

	sessionRef := req.ResumeRef
	newSession := false
	if sessionRef == "" {
		ref, err := handles.Register("codex", capd.ThreadID)
		if err != nil {
			return res, err
		}
		sessionRef, newSession = ref, true
	}
	effortState := ObservationState("")
	if req.Effort != "" {
		effortState = ObsAttested
	}
	res.Provenance = Provenance{
		// requested value is the only attestation source: nothing resolved
		// is observable on the public stream.
		ModelSelection: modelSelection(req.Model),
		RequestedModel: req.Model, ResolvedModel: "", ModelState: ObsAttested,
		RequestedEffort: req.Effort, EffortState: effortState,
		ManifestCLIVersion: a.Capability().CLIVersionChecked, ObservedCLIVersion: observedVersion,
		WorkingDir: req.WorkingDir, SessionRef: sessionRef, NewSession: newSession,
	}
	return res, nil
}
