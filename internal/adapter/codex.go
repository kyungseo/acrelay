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
	"time"
)

// CodexAdapter binds the Codex CLI (`codex exec --json`). Resolved model and
// effort are not echoed on the public structured stdout, so their
// observation state never exceeds attested.
type CodexAdapter struct{}

func (CodexAdapter) Vendor() string { return "codex" }

func (CodexAdapter) Capability() Capability {
	return Capability{
		Vendor:              "codex",
		ContractVersion:     "codex-jsonl-v1",
		KnownGoodCLIVersion: "0.144.1",
		EffortEnum:          []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		SchemaFlag:          "--output-schema",
		SupportsResume:      true, // codex exec resume <thread_id>
		ModelObservation:    ObsAttested,
		ProgressEvents:      true, // JSONL event stream
		IdleTimeoutMode:     "event-stream",
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

// detectCodexVersion parses `codex --version` ("codex-cli 0.144.1") while
// retaining the normalized banner for provenance.
func detectCodexVersion(ctx context.Context) (string, string, error) {
	banner, err := probeVersion(ctx, "codex", "--version")
	if err != nil {
		return "", "", err
	}
	return firstNonEmptyLineField(banner, 1), strings.TrimSpace(banner), nil
}

func probeCodexCapabilities(ctx context.Context) (string, string) {
	execOut, execTruncated, execErr := runBoundedProbe(ctx, 15*time.Second, "codex", "exec", "--help")
	execState, execDiag := assessHelpProbe(execOut, execTruncated, execErr, "--json", "--output-schema")
	resumeOut, resumeTruncated, resumeErr := runBoundedProbe(ctx, 15*time.Second, "codex", "exec", "resume", "--help")
	resumeState, resumeDiag := assessHelpProbe(resumeOut, resumeTruncated, resumeErr, "resume")
	return combineProbeResults([]string{execState, resumeState}, []string{execDiag, resumeDiag})
}

type codexModelObservation struct {
	model      string
	provider   string
	state      ObservationState
	source     string
	diagnostic string
}

var codexModelProbeTimeout = 5 * time.Second

func observeCodexModel(ctx context.Context, requested string) codexModelObservation {
	if requested != "" {
		return codexModelObservation{
			model: requested, state: ObsAttested, source: "codex explicit -m request",
		}
	}
	out, truncated, runErr := runBoundedProbe(ctx, codexModelProbeTimeout, "codex", "doctor", "--json")
	if truncated {
		return codexModelObservation{state: ObsUnverified, source: "codex doctor --json:config.load",
			diagnostic: "doctor output exceeded limit; raw discarded"}
	}
	var doc struct {
		Checks map[string]struct {
			Status  string `json:"status"`
			Details struct {
				Model         string `json:"model"`
				ModelProvider string `json:"model_provider"`
				Provider      string `json:"provider"`
			} `json:"details"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		diagnostic := "doctor output malformed; raw discarded"
		if runErr != nil {
			diagnostic = "doctor unavailable or timed out; raw discarded"
		}
		return codexModelObservation{state: ObsUnverified, source: "codex doctor --json:config.load",
			diagnostic: diagnostic}
	}
	config, ok := doc.Checks["config.load"]
	if !ok || config.Status != "ok" || strings.TrimSpace(config.Details.Model) == "" {
		return codexModelObservation{state: ObsUnverified, source: "codex doctor --json:config.load",
			diagnostic: "config.load model unavailable; raw discarded"}
	}
	provider := config.Details.ModelProvider
	if provider == "" {
		provider = config.Details.Provider
	}
	diagnostic := ""
	if runErr != nil {
		// The overall doctor command may report unrelated failing checks. A
		// complete config.load observation remains usable and non-authoritative.
		diagnostic = "doctor exited nonzero after usable config.load observation; unrelated checks ignored"
	}
	return codexModelObservation{model: config.Details.Model, provider: provider, state: ObsAttested,
		source: "codex doctor --json:checks.config.load.details", diagnostic: diagnostic}
}

type preparedCodex struct {
	req        Request
	handles    *HandleStore
	args       []string
	timeouts   Timeouts
	provenance Provenance
	schemaPath string
}

func (p *preparedCodex) Close() error {
	if p.schemaPath == "" {
		return nil
	}
	err := os.Remove(p.schemaPath)
	p.schemaPath = ""
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Prepare completes request/version/resume/schema preparation before the
// relay writes the dispatch journal. The returned invocation is one-shot.
func (a CodexAdapter) Prepare(ctx context.Context, req Request, handles *HandleStore) (PreparedInvocation, error) {
	if err := a.Preflight(req); err != nil {
		return nil, err
	}
	timeouts, err := req.Timeouts.Validate()
	if err != nil {
		return nil, err
	}
	observed, banner, err := detectCodexVersion(ctx)
	if err != nil {
		return nil, err
	}
	if err := PreflightVersion(a.Capability(), observed); err != nil {
		return nil, err
	}
	probeState, probeDiagnostic := probeCodexCapabilities(ctx)
	modelObservation := observeCodexModel(ctx, req.Model)
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
	schemaPath := schemaFile.Name()
	if _, err := schemaFile.WriteString(req.SchemaJSON); err != nil {
		schemaFile.Close()
		os.Remove(schemaPath)
		return nil, err
	}
	if err := schemaFile.Close(); err != nil {
		os.Remove(schemaPath)
		return nil, err
	}
	args = append(args, "--output-schema", schemaPath)
	// Prompt travels over stdin ("-" positional) so leading-dash content can
	// never be parsed as a flag.
	args = append(args, "-")
	prov := baseProvenance(a.Capability(), req, observed, banner, "codex --version",
		probeState, probeDiagnostic)
	prov.ResolvedModel = modelObservation.model
	prov.ModelProvider = modelObservation.provider
	prov.ModelState = modelObservation.state
	prov.ModelSource = modelObservation.source
	prov.ModelDiagnostic = modelObservation.diagnostic
	return &preparedCodex{req: req, handles: handles, args: args, timeouts: timeouts,
		provenance: prov, schemaPath: schemaPath}, nil
}

func (p *preparedCodex) Dispatch(ctx context.Context) (*Result, error) {
	req, handles := p.req, p.handles
	hctx, hcancel := context.WithTimeout(ctx, p.timeouts.HardCap)
	defer hcancel()
	tctx, tcancel := context.WithCancelCause(hctx)
	defer tcancel(nil)
	cmd := newGroupCmd(tctx, p.timeouts.Grace, "codex", p.args...)
	cmd.Dir = req.WorkingDir
	stdout := newWatchdogBuffer()
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = stdout, &stderr
	cmd.Stdin = bytes.NewReader([]byte(req.Prompt))
	// Event-stream mode: startup and idle are observable-output timers wired
	// to the JSONL stream (DR-811 §7).
	go superviseTimeouts(tctx, tcancel, stdout.activity, p.timeouts)
	runErr := runWithProgress(cmd, req.Progress)

	res := &Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode(cmd),
		Started: cmd.ProcessState != nil, Provenance: p.provenance,
		Diagnostic: joinDiagnostics(p.provenance.CapabilityProbeDiagnostic, p.provenance.ModelDiagnostic)}
	if !res.Started {
		return res, fmt.Errorf("codex process never started (pre-dispatch failure, no attempt consumed): %v", runErr)
	}
	switch cause := context.Cause(tctx); {
	case errors.Is(cause, ErrStartupTimeout):
		res.TimedOut, res.TimeoutKind = true, TimeoutStartup
		return res, fmt.Errorf("startup timeout after %v: FAILED(timeout:startup), no automatic retry", p.timeouts.Startup)
	case errors.Is(cause, ErrIdleTimeout):
		res.TimedOut, res.TimeoutKind = true, TimeoutIdle
		return res, fmt.Errorf("idle timeout after %v of stream silence: FAILED(timeout:idle), no automatic retry", p.timeouts.Idle)
	case hctx.Err() == context.DeadlineExceeded:
		res.TimedOut, res.TimeoutKind = true, TimeoutHardCap
		return res, fmt.Errorf("hard-cap timeout: execution UNKNOWN, re-dispatch forbidden")
	}
	capd, perr := parseCodexJSONL(stdout.Bytes())
	if perr != nil {
		return res, fmt.Errorf("dispatch capture failed (runErr=%v): FAILED, no automatic retry: %w", runErr, perr)
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
		return res, fmt.Errorf("codex stream has no structured agent message: FAILED, no automatic retry")
	} else {
		var m map[string]any
		if err := json.Unmarshal([]byte(capd.AgentMessage), &m); err != nil {
			return res, fmt.Errorf("codex structured agent message is malformed JSON: FAILED, no automatic retry: %w", err)
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
	res.Provenance.RequestedEffort = req.Effort
	res.Provenance.EffortState = effortState
	res.Provenance.SessionRef = sessionRef
	res.Provenance.NewSession = newSession
	return res, nil
}
