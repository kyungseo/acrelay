package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	VendorName   string
	NativeHandle string
	Script       []FakeResult
	PrepareFail  error
	Prepared     int
	Dispatched   int
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
		Vendor: f.VendorName, ContractVersion: "fake-v1", KnownGoodCLIVersion: "fake-1",
		EffortEnum: []string{"low", "high"}, SchemaFlag: "--fake-schema",
		SupportsResume: true, ModelObservation: ObsAttested,
		ProgressEvents: false, IdleTimeoutMode: "unsupported",
	}
}

func (f *FakeAdapter) Preflight(req Request) error {
	return validateCommonRequest(f.Capability(), req)
}

type preparedFake struct {
	adapter    *FakeAdapter
	req        Request
	handles    *HandleStore
	cleanupDir string
}

func (p *preparedFake) Close() error {
	if p.cleanupDir == "" {
		return nil
	}
	err := os.RemoveAll(p.cleanupDir)
	p.cleanupDir = ""
	return err
}

func (f *FakeAdapter) Prepare(ctx context.Context, req Request, handles *HandleStore) (PreparedInvocation, error) {
	if err := f.Preflight(req); err != nil {
		return nil, err
	}
	if f.PrepareFail != nil {
		return nil, f.PrepareFail
	}
	resumeWorkingDir := ""
	if req.ResumeRef != "" {
		vendor, _, profileID, workingDir, err := handles.Lookup(req.ResumeRef)
		if err != nil {
			return nil, err
		}
		if vendor != f.VendorName {
			return nil, fmt.Errorf("session_ref %s belongs to %s, not %s: fail-closed", req.ResumeRef, vendor, f.VendorName)
		}
		if profileID != req.TrustPolicy.ProfileID {
			return nil, fmt.Errorf("session_ref %s trust profile mismatch: explicit session reset required, fail-closed", req.ResumeRef)
		}
		resumeWorkingDir = workingDir
	}
	preparedReq, cleanupDir, err := prepareExecutionRoot(req, resumeWorkingDir)
	if err != nil {
		return nil, err
	}
	f.Prepared++
	return &preparedFake{adapter: f, req: preparedReq, handles: handles, cleanupDir: cleanupDir}, nil
}

func (p *preparedFake) Dispatch(ctx context.Context) (*Result, error) {
	f, req, handles := p.adapter, p.req, p.handles
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
		ref, err := handles.Register(f.VendorName, f.NativeHandle, req.TrustPolicy.ProfileID, req.WorkingDir)
		if err != nil {
			return res, err
		}
		p.cleanupDir = ""
		sessionRef, newSession = ref, true
	}
	mm := res.Provenance.ModelMismatch
	res.Provenance = Provenance{
		ModelSelection: modelSelection(req.Model), ModelMismatch: mm,
		RequestedModel: req.Model, ResolvedModel: req.Model, ModelState: ObsAttested,
		ModelSource: "fake adapter", RequestedEffort: req.Effort,
		AdapterContractVersion: "fake-v1", KnownGoodCLIVersion: "fake-1",
		ObservedCLIVersion: "fake-1", ObservedCLIVersionBanner: "fake-1",
		CLIVersionSource: "fake adapter", CapabilityProbeState: ProbeObserved,
		CapabilityProbeDiagnostic: "fake required capabilities observed",
		WorkingDir:                req.WorkingDir, WorkingDirMode: req.TrustPolicy.WorkingDirMode,
		SubjectRoot: req.SubjectRoot, TrustProfileID: req.TrustPolicy.ProfileID,
		RestrictionEvidenceState: ObsVerified, EgressApprovalID: EgressApprovalID,
		EgressApprovalRecorded: true,
		SessionRef:             sessionRef, NewSession: newSession,
	}
	return res, nil
}
