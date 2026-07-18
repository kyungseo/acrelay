package adapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	return ValidateEffort(a.Capability(), req.Effort)
}

// codexCapture is the parse result of one JSONL stream.
type codexCapture struct {
	ThreadID     string
	AgentMessage string
	TurnFailed   bool
	FailReason   string
}

// parseCodexJSONL parses the event stream. Malformed event lines fail
// closed with an explicit diagnostic — they are never skipped silently.
func parseCodexJSONL(stdout []byte) (*codexCapture, error) {
	cap := &codexCapture{}
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
			cap.ThreadID = ev.Thread
		case "item.completed":
			if ev.Item.Type == "agent_message" {
				cap.AgentMessage = ev.Item.Text
			}
		case "turn.failed":
			cap.TurnFailed = true
			cap.FailReason = ev.Error.Message
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("codex JSONL scan: %w", err)
	}
	return cap, nil
}

func (a CodexAdapter) Dispatch(ctx context.Context, req Request, handles *HandleStore) (*Result, error) {
	if err := a.Preflight(req); err != nil {
		return nil, err
	}
	args := []string{"exec"}
	var resumeHandle string
	if req.ResumeRef != "" {
		vendor, h, err := handles.Lookup(req.ResumeRef)
		if err != nil {
			return nil, err
		}
		if vendor != "codex" {
			return nil, fmt.Errorf("session_ref %s belongs to %s, not codex: fail-closed", req.ResumeRef, vendor)
		}
		resumeHandle = h
		args = append(args, "resume", resumeHandle)
	}
	args = append(args, "--skip-git-repo-check", "--json")
	if req.Model != "" {
		args = append(args, "-m", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+req.Effort)
	}
	var schemaPath string
	if req.SchemaJSON != "" {
		f, err := os.CreateTemp("", "acrelay-schema-*.json")
		if err != nil {
			return nil, err
		}
		schemaPath = f.Name()
		defer os.Remove(schemaPath)
		if _, err := f.WriteString(req.SchemaJSON); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
		args = append(args, "--output-schema", filepath.Clean(schemaPath))
	}
	// Prompt travels over stdin ("-" positional) so leading-dash content can
	// never be parsed as a flag.
	args = append(args, "-")

	tctx, cancel := context.WithTimeout(ctx, req.Timeouts.HardCap)
	defer cancel()
	cmd := newGroupCmd(tctx, "codex", args...)
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
	capd, perr := parseCodexJSONL(stdout.Bytes())
	if perr != nil {
		return res, fmt.Errorf("dispatch capture failed (runErr=%v): %w", runErr, perr)
	}
	if capd.TurnFailed || (runErr != nil && res.ExitCode != 0) {
		return res, fmt.Errorf("codex turn failed (exit=%d, reason=%.120s): FAILED", res.ExitCode, capd.FailReason)
	}
	if req.SchemaJSON != "" && capd.AgentMessage != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(capd.AgentMessage), &m); err == nil {
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
	effortState := ObsUnsupported
	if req.Effort != "" {
		effortState = ObsAttested
	}
	res.Provenance = Provenance{
		RequestedModel: req.Model, ResolvedModel: "", ModelState: ObsAttested,
		RequestedEffort: req.Effort, EffortState: effortState,
		CLIVersion: a.Capability().CLIVersionChecked, WorkingDir: req.WorkingDir,
		SessionRef: sessionRef, NewSession: newSession,
	}
	return res, nil
}
