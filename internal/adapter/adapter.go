// Package adapter maps platform CLIs onto the kernel contract: one-shot
// dispatch, structured capture, provenance with observation states, static
// capability manifests versioned per adapter+CLI version, and the private
// handle store for session continuity (DR-811 §3, §7).
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ObservationState qualifies every resolved value: nothing observed is ever
// presented as verified. An omitted request records an empty state — the
// absence of a request is distinct from "unsupported" (R0-CX-N3).
type ObservationState string

const (
	ObsVerified    ObservationState = "verified"
	ObsAttested    ObservationState = "attested"
	ObsUnverified  ObservationState = "unverified"
	ObsUnsupported ObservationState = "unsupported"
)

// Capability is the static manifest for one adapter at one CLI version.
// Any other observed version fails closed at preflight (DR-811 §7).
type Capability struct {
	Vendor            string
	CLIVersionChecked string
	EffortEnum        []string
	SchemaFlag        string
	SupportsResume    bool
	ModelObservation  ObservationState
	ProgressEvents    bool
	IdleTimeoutMode   string // "event-stream" or "unsupported"
}

// PreflightVersion compares the observed CLI version against the manifest.
// Unknown versions fail closed: the manifest's guarantees were validated
// against exactly one version.
func PreflightVersion(cap Capability, observed string) error {
	if strings.TrimSpace(observed) == "" {
		return fmt.Errorf("%s CLI version unobservable: fail-closed", cap.Vendor)
	}
	if observed != cap.CLIVersionChecked {
		return fmt.Errorf("%s CLI version %s not validated against manifest (%s): fail-closed",
			cap.Vendor, observed, cap.CLIVersionChecked)
	}
	return nil
}

// Timeouts decomposes the invocation deadline contract (DR-811 §7).
type Timeouts struct {
	Startup time.Duration // dispatch → first observable output (event-stream wiring: C4)
	Idle    time.Duration // max silence between events (event-stream only)
	HardCap time.Duration // absolute wall-clock bound
	Grace   time.Duration // SIGTERM → SIGKILL escalation window
}

// DefaultTimeouts are the v1 numeric defaults (FEAT-20260718-002).
func DefaultTimeouts() Timeouts {
	return Timeouts{
		Startup: 120 * time.Second,
		Idle:    300 * time.Second,
		HardCap: 30 * time.Minute,
		Grace:   10 * time.Second,
	}
}

// Validate normalizes zero fields to defaults and enforces positivity and
// ordering (grace < idle < hard-cap). Invalid explicit values fail closed.
func (t Timeouts) Validate() (Timeouts, error) {
	d := DefaultTimeouts()
	if t.Startup == 0 {
		t.Startup = d.Startup
	}
	if t.Idle == 0 {
		t.Idle = d.Idle
	}
	if t.HardCap == 0 {
		t.HardCap = d.HardCap
	}
	if t.Grace == 0 {
		t.Grace = d.Grace
	}
	if t.Startup < 0 || t.Idle < 0 || t.HardCap < 0 || t.Grace < 0 {
		return t, fmt.Errorf("timeout components must be positive: %+v", t)
	}
	if !(t.Grace < t.Idle && t.Idle < t.HardCap) {
		return t, fmt.Errorf("timeout ordering must be grace < idle < hard-cap: %+v", t)
	}
	return t, nil
}

// Startup/idle timers are observable-output timers: they only exist where the
// CLI emits an event stream (IdleTimeoutMode "event-stream"). Final-envelope
// CLIs produce no observable output before the terminal envelope, so startup
// and idle are both undefined there and only the hard-cap applies (DR-811 §7
// declares the idle carve-out; startup follows the same observability rule).
// Startup/idle expiry is FAILED(timeout:*) — no terminal output was captured
// and no automatic retry happens. Hard-cap expiry stays UNKNOWN.
var (
	ErrStartupTimeout = errors.New("startup timeout: no observable output before deadline")
	ErrIdleTimeout    = errors.New("idle timeout: event stream went silent past deadline")
)

// watchdogBuffer is a bytes.Buffer that reports event activity to the
// timeout supervisor. Activity means a complete, parseable JSONL event
// (newline-terminated JSON object) — DR-811 defines idle as silence between
// *events*, so a partial-line byte trickle or malformed fragment never
// resets the timers. The activity channel is buffered and never blocks the
// child's output pipe.
type watchdogBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	line     []byte // partial-line accumulator for event detection
	activity chan struct{}
}

func newWatchdogBuffer() *watchdogBuffer {
	return &watchdogBuffer{activity: make(chan struct{}, 1)}
}

func (w *watchdogBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	w.line = append(w.line, p[:n]...)
	for {
		i := bytes.IndexByte(w.line, '\n')
		if i < 0 {
			break
		}
		candidate := bytes.TrimSpace(w.line[:i])
		w.line = append([]byte(nil), w.line[i+1:]...)
		if len(candidate) > 0 && candidate[0] == '{' && json.Valid(candidate) {
			select {
			case w.activity <- struct{}{}:
			default:
			}
		}
	}
	return n, err
}

func (w *watchdogBuffer) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Bytes()
}

// superviseTimeouts cancels the dispatch context with ErrStartupTimeout when
// no output arrives within t.Startup, then with ErrIdleTimeout whenever the
// stream stays silent longer than t.Idle. It exits when ctx ends.
func superviseTimeouts(ctx context.Context, cancel context.CancelCauseFunc, activity <-chan struct{}, t Timeouts) {
	startup := time.NewTimer(t.Startup)
	defer startup.Stop()
	select {
	case <-ctx.Done():
		return
	case <-startup.C:
		cancel(ErrStartupTimeout)
		return
	case <-activity:
	}
	idle := time.NewTimer(t.Idle)
	defer idle.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-idle.C:
			cancel(ErrIdleTimeout)
			return
		case <-activity:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(t.Idle)
		}
	}
}

// Request is one reviewer invocation. SchemaJSON is mandatory: schema
// enforcement is part of dispatch, not an option (DR-811 §5).
type Request struct {
	Prompt     string
	Model      string // "" = platform default
	Effort     string // "" = omitted (no flag is sent)
	SchemaJSON string
	WorkingDir string
	ResumeRef  string // session_ref to resume, "" = new session
	Timeouts   Timeouts
}

// Provenance records what was requested and what was observed.
type Provenance struct {
	ModelSelection     string // "explicit" or "platform-default" (R1-CX-N2)
	RequestedModel     string
	ResolvedModel      string
	ModelState         ObservationState
	ModelMismatch      bool // explicit model does not match resolved model
	RequestedEffort    string
	EffortState        ObservationState // empty when effort was omitted
	ManifestCLIVersion string
	ObservedCLIVersion string
	WorkingDir         string
	SessionRef         string // opaque random reference — never the native handle
	NewSession         bool
}

// TimeoutKind distinguishes which deadline expired. Startup/idle expiry is
// FAILED — no terminal output was ever captured. Hard-cap expiry is UNKNOWN —
// the invocation was cut at the wall clock and may have progressed (DR-811
// §7). The relay maps execution state from this kind, never from the bare
// TimedOut flag.
const (
	TimeoutStartup = "startup"
	TimeoutIdle    = "idle"
	TimeoutHardCap = "hard-cap"
)

// Result is the transport-level outcome of one dispatch.
type Result struct {
	Stdout      []byte
	Stderr      []byte
	Started     bool // child process actually started (R1-CX-F7)
	ExitCode    int  // -1 when the process never started
	Structured  map[string]any
	Diagnostic  string   // e.g. non-JSON prefix note — preserved, never dropped
	Invalid     []string // validation notes that classify the outcome as needs-input
	Provenance  Provenance
	TimedOut    bool
	TimeoutKind string // TimeoutStartup | TimeoutIdle | TimeoutHardCap, "" when !TimedOut
}

// Adapter is one platform binding.
type Adapter interface {
	Vendor() string
	Capability() Capability
	// Preflight validates the request against the static manifest before
	// any dispatch and before any attempt is committed. Explicit-but-
	// unsupported inputs fail here — never silently ignored.
	Preflight(req Request) error
	// PreDispatch runs every non-consuming check (preflight, CLI version
	// probe, resume-ref resolution). A failure here must never consume a
	// round or attempt (R0-CX-F3) — the relay commits the attempt only
	// after PreDispatch succeeds.
	PreDispatch(ctx context.Context, req Request, handles *HandleStore) error
	// Dispatch runs exactly one child invocation. It never retries.
	Dispatch(ctx context.Context, req Request, handles *HandleStore) (*Result, error)
}

// ValidateEffort implements the shared pre-dispatch effort check.
func ValidateEffort(cap Capability, effort string) error {
	if effort == "" {
		return nil // omitted — no flag will be sent
	}
	for _, e := range cap.EffortEnum {
		if e == effort {
			return nil
		}
	}
	return fmt.Errorf("explicit effort %q unsupported by %s (enum %v): pre-dispatch failure",
		effort, cap.Vendor, cap.EffortEnum)
}

// validateCommonRequest applies the vendor-independent preflight checks.
func validateCommonRequest(cap Capability, req Request) error {
	if err := ValidateEffort(cap, req.Effort); err != nil {
		return err
	}
	if strings.TrimSpace(req.SchemaJSON) == "" {
		return fmt.Errorf("SchemaJSON is mandatory: schema enforcement is part of dispatch (DR-811 §5)")
	}
	if _, err := req.Timeouts.Validate(); err != nil {
		return err
	}
	return nil
}

// probeVersion runs the CLI's version command outside any round/attempt
// budget and returns raw stdout.
func probeVersion(ctx context.Context, name string, args ...string) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(pctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s version probe failed: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// newGroupCmd builds an exec.Cmd whose child runs in an isolated,
// terminable lifecycle boundary. On POSIX this is a new process group; the
// Windows Job Object equivalent is a follow-up platform port (DR-811).
// On cancellation the whole group gets SIGTERM, then SIGKILL after grace —
// grandchildren that ignore SIGTERM do not survive (R0-CX-F8).
func newGroupCmd(ctx context.Context, grace time.Duration, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		pgid := cmd.Process.Pid
		err := syscall.Kill(-pgid, syscall.SIGTERM)
		time.AfterFunc(grace, func() {
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		})
		return err
	}
	cmd.WaitDelay = grace + 2*time.Second // backstop for the direct child
	return cmd
}

// modelSelection classifies the request kind for provenance.
func modelSelection(requested string) string {
	if requested == "" {
		return "platform-default"
	}
	return "explicit"
}

// exitCode reads the exit code defensively: a process that never started
// has no ProcessState (R0-CX-F7).
func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// firstNonEmptyLineField splits a version banner and returns the field at
// idx of its first nonempty line, or "".
func firstNonEmptyLineField(banner string, idx int) string {
	for _, line := range strings.Split(banner, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) > idx {
			return fields[idx]
		}
		if len(fields) > 0 {
			return ""
		}
	}
	return ""
}
