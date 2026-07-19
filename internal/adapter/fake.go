package adapter

import (
	"context"
	"encoding/json"
	"fmt"
)

// FakeResult scripts one dispatch outcome for deterministic fixtures.
type FakeResult struct {
	Structured    map[string]any
	Invalid       []string
	Err           error
	TimedOut      bool
	TimeoutKind   string // TimeoutStartup | TimeoutIdle | TimeoutHardCap ("" defaults to hard-cap)
	StartFailure  bool   // child never started (no attempt consumed)
	ModelMismatch bool
}

// FakeAdapter is the deterministic test double: it honors the real
// interface contract (preflight, handle registration, resume validation)
// without spawning processes or calling vendors.
type FakeAdapter struct {
	VendorName      string
	NativeHandle    string
	Script          []FakeResult
	PreDispatchFail error
	Dispatched      int
	// Test synchronization hooks (nil in normal use). When set, Dispatch
	// closes DispatchEntered on entry — after the caller has already taken
	// its pre-dispatch snapshot — and then blocks until DispatchGate is
	// closed, letting a test interleave another writer deterministically.
	DispatchEntered chan struct{}
	DispatchGate    <-chan struct{}
}

func (f *FakeAdapter) Vendor() string { return f.VendorName }

func (f *FakeAdapter) Capability() Capability {
	return Capability{
		Vendor: f.VendorName, CLIVersionChecked: "fake-1",
		EffortEnum: []string{"low", "high"}, SchemaFlag: "--fake-schema",
		SupportsResume: true, ModelObservation: ObsAttested,
		ProgressEvents: false, IdleTimeoutMode: "unsupported",
	}
}

func (f *FakeAdapter) Preflight(req Request) error {
	return validateCommonRequest(f.Capability(), req)
}

func (f *FakeAdapter) PreDispatch(ctx context.Context, req Request, handles *HandleStore) error {
	if err := f.Preflight(req); err != nil {
		return err
	}
	if f.PreDispatchFail != nil {
		return f.PreDispatchFail
	}
	if req.ResumeRef != "" {
		vendor, _, err := handles.Lookup(req.ResumeRef)
		if err != nil {
			return err
		}
		if vendor != f.VendorName {
			return fmt.Errorf("session_ref %s belongs to %s, not %s: fail-closed", req.ResumeRef, vendor, f.VendorName)
		}
	}
	return nil
}

func (f *FakeAdapter) Dispatch(ctx context.Context, req Request, handles *HandleStore) (*Result, error) {
	if f.DispatchEntered != nil {
		close(f.DispatchEntered)
		f.DispatchEntered = nil
	}
	if f.DispatchGate != nil {
		<-f.DispatchGate
	}
	if f.Dispatched >= len(f.Script) {
		return nil, fmt.Errorf("fake script exhausted after %d dispatches", f.Dispatched)
	}
	fr := f.Script[f.Dispatched]
	f.Dispatched++

	raw, _ := json.Marshal(fr.Structured)
	res := &Result{Stdout: raw, Stderr: nil, ExitCode: 0, Structured: fr.Structured,
		Invalid: fr.Invalid, TimedOut: fr.TimedOut, Started: !fr.StartFailure}
	res.Provenance.ModelMismatch = fr.ModelMismatch
	if fr.StartFailure {
		res.ExitCode = -1
		return res, fmt.Errorf("fake process never started (pre-dispatch failure, no attempt consumed)")
	}
	if req.Progress != nil {
		req.Progress("running", "reviewer process started")
	}
	if fr.TimedOut {
		res.TimeoutKind = fr.TimeoutKind
		if res.TimeoutKind == "" {
			res.TimeoutKind = TimeoutHardCap
		}
		if res.TimeoutKind == TimeoutHardCap {
			return res, fmt.Errorf("hard-cap timeout: execution UNKNOWN, re-dispatch forbidden")
		}
		return res, fmt.Errorf("%s timeout: FAILED(timeout:%s), no automatic retry", res.TimeoutKind, res.TimeoutKind)
	}
	if fr.Err != nil {
		return res, fr.Err
	}
	sessionRef := req.ResumeRef
	newSession := false
	if sessionRef == "" {
		ref, err := handles.Register(f.VendorName, f.NativeHandle)
		if err != nil {
			return res, err
		}
		sessionRef, newSession = ref, true
	}
	mm := res.Provenance.ModelMismatch
	res.Provenance = Provenance{
		ModelSelection: modelSelection(req.Model), ModelMismatch: mm,
		RequestedModel: req.Model, ModelState: ObsAttested,
		RequestedEffort: req.Effort,
		ManifestCLIVersion: "fake-1", ObservedCLIVersion: "fake-1",
		SessionRef: sessionRef, NewSession: newSession,
	}
	return res, nil
}
