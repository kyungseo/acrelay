// Package adapter maps platform CLIs onto the kernel contract: one-shot
// dispatch, structured capture, provenance with observation states, static
// capability manifests versioned per adapter+CLI version, and the private
// handle store for session continuity (DR-811 §3, §7).
package adapter

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// ObservationState qualifies every resolved value: nothing observed is ever
// presented as verified.
type ObservationState string

const (
	ObsVerified    ObservationState = "verified"
	ObsAttested    ObservationState = "attested"
	ObsUnverified  ObservationState = "unverified"
	ObsUnsupported ObservationState = "unsupported"
)

// Capability is the static manifest for one adapter at one CLI version
// range. Unknown CLI versions fail closed at preflight.
type Capability struct {
	Vendor            string
	CLIVersionChecked string // version the manifest was validated against
	EffortEnum        []string
	SchemaFlag        string // structured output enforcement flag
	SupportsResume    bool
	ModelObservation  ObservationState // best achievable for resolved model
	ProgressEvents    bool             // event-stream surface available
	IdleTimeoutMode   string           // "event-stream" or "unsupported"
}

// Timeouts decomposes the invocation deadline contract (DR-811 §7).
// Numeric defaults are fixed here per the DR-811 handover; every field is
// overridable per request.
type Timeouts struct {
	Startup time.Duration // dispatch → first observable output
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

// Request is one reviewer invocation.
type Request struct {
	Prompt     string
	Model      string // "" = platform default
	Effort     string // "" = omitted (no flag is sent)
	SchemaJSON string // canonical ReviewResult schema
	WorkingDir string
	ResumeRef  string // session_ref to resume, "" = new session
	Timeouts   Timeouts
}

// Provenance records what was requested and what was observed.
type Provenance struct {
	RequestedModel  string
	ResolvedModel   string
	ModelState      ObservationState
	RequestedEffort string // "" means omitted
	EffortState     ObservationState
	CLIVersion      string
	WorkingDir      string
	SessionRef      string // opaque random reference — never the native handle
	NewSession      bool
}

// Result is the transport-level outcome of one dispatch.
type Result struct {
	Stdout     []byte
	Stderr     []byte
	ExitCode   int
	Structured map[string]any
	Provenance Provenance
	// TimedOut marks a hard-cap/startup/idle kill; the side effect on the
	// vendor side is UNKNOWN and re-dispatch is forbidden.
	TimedOut bool
}

// Adapter is one platform binding.
type Adapter interface {
	Vendor() string
	Capability() Capability
	// Preflight validates the request against the static manifest before
	// any dispatch. Explicit-but-unsupported inputs fail here — never
	// silently ignored (Claude CLI silent-ignore hazard, DR-811 §7).
	Preflight(req Request) error
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

// newGroupCmd builds an exec.Cmd whose child runs in an isolated,
// terminable lifecycle boundary. On POSIX this is a new process group; the
// Windows Job Object equivalent is a follow-up platform port (DR-811).
func newGroupCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Negative pid targets the whole process group.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	cmd.WaitDelay = DefaultTimeouts().Grace // SIGKILL escalation after grace
	return cmd
}
