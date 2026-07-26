// Command acrelay is the acRelay one-shot review relay CLI.
// It automates the reviewer leg only: request assembly, one child
// invocation, capture, validation, and canonical-record append. Driver
// dispositions and arbiter decisions stay human commands.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/platform"
	"github.com/kyungseo/acrelay/internal/relay"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/subject"
)

// dispatchSignalContext implements the parent-signal contract for dispatching
// commands: the first SIGINT/SIGTERM cancels the
// dispatch context with adapter.ErrParentSignal (graceful group
// SIGTERM->grace->SIGKILL, classified canceled.parent-signal, journal stays
// authoritative); a second signal force-kills every tracked child group and
// exits immediately. Signals are consumed from the injected channel so the
// two-stage policy is unit-testable without real signals.
func dispatchSignalContext(parent context.Context, signals <-chan os.Signal, forceExit func()) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancelCause(parent)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			cancel(adapter.ErrParentSignal)
		}
		select {
		case <-signals:
			adapter.ForceKillActiveProcessGroups()
			forceExit()
		case <-time.After(10 * time.Minute): // dispatch teardown backstop; goroutine exits with process anyway
		}
	}()
	return ctx, func() { cancel(nil) }
}

func newDispatchContext() (context.Context, context.CancelFunc) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, platform.InterruptSignals()...)
	return dispatchSignalContext(context.Background(), signals, func() {
		fmt.Fprintln(os.Stderr, "second signal: child groups force-killed; reconcile the pending journal before further mutation")
		os.Exit(130)
	})
}

// resolveHandles returns the handle store path. An explicit -handles value
// is used as-is (private verification still happens in the store); the
// default resolution fails closed — home or private-directory failure is a
// preflight error, never a silent cwd fallback that could lose the
// persisted reviewer binding (R1-CX-F3).
func resolveHandlesPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("default handle store unavailable (no user home): pass -handles explicitly: %w", err)
	}
	return filepath.Join(home, ".acrelay", "handles.json"), nil
}

func resolveHandles(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	path, err := resolveHandlesPath(explicit)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := platform.MkdirPrivate(dir); err != nil {
		return "", fmt.Errorf("default handle store directory %s is not usable as private storage: fix it or pass -handles explicitly: %w", dir, err)
	}
	return path, nil
}

func adapterFor(name string) (adapter.Adapter, error) {
	switch name {
	case "claude":
		return adapter.ClaudeAdapter{}, nil
	case "codex":
		return adapter.CodexAdapter{}, nil
	}
	return nil, fmt.Errorf("unknown reviewer %q (claude|codex)", name)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func parseOptionalFormalRoundBound(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	bound, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("round-bound must be an integer in %d..%d",
			kernel.MinFormalRoundBound, kernel.MaxFormalRoundBound)
	}
	if err := kernel.ValidateFormalRoundBound(bound); err != nil {
		return 0, err
	}
	return bound, nil
}

func parseSubjectInput(target, targetSpec string) (subject.Spec, error) {
	if (target == "") == (targetSpec == "") {
		return subject.Spec{}, fmt.Errorf("init requires exactly one of -target or -target-spec")
	}
	if target != "" {
		return subject.SingleFile(target)
	}
	return subject.LoadSpec(targetSpec)
}

func decodeJSONFile(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing JSON value in %s", path)
	}
	return nil
}

func runBriefing(args []string, stdout io.Writer) (int, error) {
	fs := flag.NewFlagSet("briefing", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	canonical := fs.String("canonical", "", "canonical record path")
	format := fs.String("format", "human", "human|json")
	check := fs.Bool("check", false, "return the stable readiness exit classification")
	if err := fs.Parse(args); err != nil {
		return 0, err
	}
	if *canonical == "" {
		return 0, fmt.Errorf("briefing requires -canonical")
	}
	briefing, err := relay.BuildBriefing(*canonical)
	if err != nil {
		return 0, err
	}
	switch *format {
	case "human":
		if _, err := io.WriteString(stdout, relay.RenderBriefingHuman(briefing)); err != nil {
			return 0, err
		}
	case "json":
		encoded, err := relay.MarshalBriefingJSON(briefing)
		if err != nil {
			return 0, err
		}
		if _, err := fmt.Fprintln(stdout, string(encoded)); err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("briefing format must be human or json")
	}
	if *check {
		return relay.BriefingCheckExitCode(briefing.Readiness), nil
	}
	return 0, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: acrelay <init|review|confirm|disposition|request-approval|respond-approval|withdraw-approval|advance|close|terminate|reconcile|abandon-transaction|cleanup|status|briefing|version> [flags]
The canonical record is private local storage: keep it outside shared/synced/
published paths. Raw canonical sharing is unsupported; redacted export is not provided in v1.`)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "init":
		fs := flag.NewFlagSet("init", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path (private storage)")
		question := fs.String("question", "", "review objective question")
		target := fs.String("target", "", "single-file subject shorthand")
		targetSpec := fs.String("target-spec", "", "JSON subject spec (file|files|subtree; mutually exclusive with -target)")
		prior := fs.String("prior", "", "prior objective ID (same-target follow-up)")
		diff := fs.String("material-diff", "", "material difference vs prior objective")
		seen := fs.Bool("seen-before", false, "target manifest was reviewed before")
		approvalActor := fs.String("approval-actor", "", "declared owner identity for trust approvals")
		ackEgress := fs.Bool("ack-vendor-egress", false, "approve vendor processing of content, absolute/resolved paths, and metadata")
		inTargetWorkdir := fs.Bool("allow-in-target-workdir", false, "approve unsafe reviewer cwd inside subject (code-execution/read/egress risk)")
		executionSurface := fs.String("execution-surface", "", "reviewer execution surface (external-cli; host-subagent is explicitly unsupported)")
		driverVendor := fs.String("driver-vendor", "", "operator-declared driver agent vendor (claude|codex|other; omitted records undeclared)")
		contextRelation := fs.String("context-relation", "", "operator-declared driver/reviewer context relation (separate|shared; omitted records undeclared)")
		allowUnsafeLocation := fs.Bool("allow-unsafe-location", false, "one-shot override for a detected VCS/supported sync location")
		unsafeLocationReason := fs.String("unsafe-location-reason", "", "non-empty owner rationale for -allow-unsafe-location")
		fs.Parse(args)
		if *canonical == "" || *question == "" {
			fail(fmt.Errorf("init requires -canonical and -question"))
		}
		spec, err := parseSubjectInput(*target, *targetSpec)
		if err != nil {
			fail(err)
		}
		policy, err := adapter.NewTrustPolicy(*approvalActor, *ackEgress, *inTargetWorkdir)
		if err != nil {
			fail(err)
		}
		topology, err := relay.NewTopologyPolicy(*executionSurface, *driverVendor, *contextRelation)
		if err != nil {
			fail(err)
		}
		var locationOverride *relay.LocationOverride
		if *allowUnsafeLocation || *unsafeLocationReason != "" {
			if !*allowUnsafeLocation {
				fail(fmt.Errorf("-unsafe-location-reason requires -allow-unsafe-location"))
			}
			locationOverride = &relay.LocationOverride{Actor: *approvalActor, Rationale: *unsafeLocationReason}
		}
		st, err := relay.InitSubjectTopologyWithLocation(*canonical, *question, spec, *prior, *diff, *seen, policy, topology, locationOverride)
		if err != nil {
			fail(err)
		}
		fmt.Printf("objective %s (collaboration %s) initialized\n", st.ObjectiveID, st.CollaborationID)

	case "review":
		fs := flag.NewFlagSet("review", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		reviewer := fs.String("reviewer", "", "claude|codex")
		prompt := fs.String("prompt", "", "review request prompt")
		promptFile := fs.String("prompt-file", "", "read prompt from file")
		model := fs.String("model", "", "explicit model (default: platform default)")
		effort := fs.String("effort", "", "explicit effort (default: omitted, no flag sent)")
		roundBound := fs.String("round-bound", "", "objective formal round bound 1..5 (first review default: 3)")
		handles := fs.String("handles", "", "session handle store path (default: ~/.acrelay/handles.json, fail-closed)")
		workdir := fs.String("workdir", "", "reviewer cwd (default: private neutral temp root; in-target requires init approval)")
		resetMode := fs.String("session-reset", "", "second-opinion|context-reset|resume-failure|unrelated")
		resetReason := fs.String("session-reset-reason", "", "reason for the session reset")
		fs.Parse(args)
		if *canonical == "" || *reviewer == "" {
			fail(fmt.Errorf("review requires -canonical, -reviewer"))
		}
		p := *prompt
		if *promptFile != "" {
			b, err := os.ReadFile(*promptFile)
			if err != nil {
				fail(err)
			}
			p = string(b)
		}
		if p == "" {
			fail(fmt.Errorf("review requires -prompt or -prompt-file"))
		}
		a, err := adapterFor(*reviewer)
		if err != nil {
			fail(err)
		}
		bound, err := parseOptionalFormalRoundBound(*roundBound)
		if err != nil {
			fail(err)
		}
		handlesPath, err := resolveHandles(*handles)
		if err != nil {
			fail(err)
		}
		s := &relay.Session{
			Adapter: a, Handles: &adapter.HandleStore{Path: handlesPath}, Canonical: *canonical,
			FormalRoundBound: bound,
		}
		if *resetMode != "" || *resetReason != "" {
			s.Reset = &relay.SessionReset{Mode: *resetMode, Reason: *resetReason}
		}
		// Surface every observable reviewer state (started/running/completed/
		// failed/unknown) with reviewer identity, on success and failure alike.
		s.Reporter = func(state, detail string) {
			fmt.Fprintf(os.Stderr, "progress: %s %s\n", state, detail)
		}
		dispatchCtx, cancelDispatch := newDispatchContext()
		defer cancelDispatch()
		st, outcome, err := s.Review(dispatchCtx, p,
			adapter.Request{Model: *model, Effort: *effort, WorkingDir: *workdir})
		if err != nil {
			fail(err) // the "failed"/"unknown" progress line was already emitted
		}
		last := st.Rounds[len(st.Rounds)-1]
		fmt.Printf("round R%d: outcome=%s verdict=%s governance=%s\n", last.Index, outcome, last.Verdict, st.Governance)
		for _, f := range st.Findings {
			if f.Disposition == "" {
				fmt.Printf("  finding %s [blocking=%v]: %s\n", f.ID, f.Blocking, f.Summary)
			}
		}

	case "disposition":
		fs := flag.NewFlagSet("disposition", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		finding := fs.String("finding", "", "finding ID")
		decision := fs.String("decision", "", "accept|revise|defend|needs-user")
		rationale := fs.String("rationale", "", "driver rationale (required for every disposition)")
		followUp := fs.String("follow-up", "", "follow-up action; accept/revise require a value or no-action")
		requestID := fs.String("request-id", "", "approval request ID (needs-user only)")
		fs.Parse(args)
		st, err := relay.Disposition(*canonical, *finding, review.DispositionInput{
			Decision: review.Disposition(*decision), Rationale: *rationale,
			FollowUp: *followUp, ApprovalRequestID: *requestID,
		})
		if err != nil {
			fail(err)
		}
		fmt.Printf("finding %s -> %s (governance=%s)\n", *finding, *decision, st.Governance)

	case "request-approval":
		fs := flag.NewFlagSet("request-approval", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		requestFile := fs.String("request-file", "", "JSON typed request with type/scope/reason/options")
		role := fs.String("role", "driver", "requester role: driver|reviewer")
		requester := fs.String("requester", "", "declared requester identity")
		fs.Parse(args)
		if *canonical == "" || *requestFile == "" || *requester == "" {
			fail(fmt.Errorf("request-approval requires -canonical, -request-file, and -requester"))
		}
		var input review.ApprovalRequestInput
		if err := decodeJSONFile(*requestFile, &input); err != nil {
			fail(err)
		}
		st, request, err := relay.RequestApproval(*canonical, input, *role, *requester)
		if err != nil {
			fail(err)
		}
		fmt.Printf("approval request %s OPEN (governance=%s)\n", request.ID, st.Governance)

	case "respond-approval":
		fs := flag.NewFlagSet("respond-approval", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		requestID := fs.String("request", "", "stable approval request ID")
		actor := fs.String("actor", "", "declared owner/arbiter identity")
		verbatim := fs.String("verbatim", "", "exact owner response text")
		verbatimFile := fs.String("verbatim-file", "", "read exact owner response text from file")
		respondedAt := fs.String("responded-at", "", "owner response date/time")
		decision := fs.String("decision", "", "exact request option ID")
		scope := fs.String("scope", "", "exact decision scope")
		anchor := fs.String("durable-anchor", "", "durable source/relay anchor for the owner response")
		unambiguous := fs.Bool("unambiguous", false, "declare that verbatim, option, and scope are explicit and unconditional")
		fs.Parse(args)
		raw := *verbatim
		if *verbatimFile != "" {
			if raw != "" {
				fail(fmt.Errorf("use exactly one of -verbatim or -verbatim-file"))
			}
			b, err := os.ReadFile(*verbatimFile)
			if err != nil {
				fail(err)
			}
			raw = string(b)
		}
		if *canonical == "" || *requestID == "" {
			fail(fmt.Errorf("respond-approval requires -canonical and -request"))
		}
		st, resolved, err := relay.RespondApproval(*canonical, *requestID, review.OwnerResponse{
			Actor: *actor, Verbatim: raw, RespondedAt: *respondedAt, Decision: *decision,
			DecisionScope: *scope, DurableAnchor: *anchor, Unambiguous: *unambiguous,
		})
		if err != nil {
			fail(err)
		}
		fmt.Printf("approval request %s resolved=%v (governance=%s)\n", *requestID, resolved, st.Governance)

	case "withdraw-approval":
		fs := flag.NewFlagSet("withdraw-approval", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		requestID := fs.String("request", "", "stable approval request ID")
		actor := fs.String("actor", "", "declared owner/arbiter identity")
		reason := fs.String("reason", "", "why this request is obsolete")
		fs.Parse(args)
		st, err := relay.WithdrawApproval(*canonical, *requestID, *actor, *reason)
		if err != nil {
			fail(err)
		}
		fmt.Printf("approval request %s WITHDRAWN (governance=%s)\n", *requestID, st.Governance)

	case "close":
		fs := flag.NewFlagSet("close", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		actor := fs.String("actor", "", "who is closing (accountability)")
		role := fs.String("role", "owner", "actor role: owner|driver|...")
		authority := fs.String("authority", "", "bounded-delegation authority basis (required for non-owner roles)")
		fs.Parse(args)
		st, err := relay.Close(*canonical, *actor, *role, *authority)
		if err != nil {
			fail(err)
		}
		fmt.Printf("objective %s CLOSED (actor=%s role=%s)\n", st.ObjectiveID, st.CloseActor, st.CloseRole)

	case "advance":
		fs := flag.NewFlagSet("advance", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		note := fs.String("note", "", "what changed in the target (delta summary)")
		fs.Parse(args)
		st, err := relay.Advance(*canonical, *note)
		if err != nil {
			fail(err)
		}
		a := st.Advances[len(st.Advances)-1]
		fmt.Printf("objective %s advanced after R%d (governance=%s)\n", st.ObjectiveID, a.AfterRound, st.Governance)

	case "terminate":
		fs := flag.NewFlagSet("terminate", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		to := fs.String("to", "", "ABANDONED|SUPERSEDED")
		arbiter := fs.String("arbiter", "", "arbiter identity")
		reason := fs.String("reason", "", "termination reason")
		fs.Parse(args)
		st, err := relay.Terminate(*canonical, kernel.GovernanceState(*to), *arbiter, *reason)
		if err != nil {
			fail(err)
		}
		fmt.Printf("objective %s %s\n", st.ObjectiveID, st.Governance)

	case "confirm":
		fs := flag.NewFlagSet("confirm", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		round := fs.Int("round", -1, "formal round index (R0=0)")
		open := fs.String("open", "", "comma-separated closed finding IDs to open a cycle")
		submit := fs.String("submit", "", "comma-separated IDs to submit to the reviewer")
		delta := fs.String("delta", "", "claimed delta description")
		expected := fs.String("expected-target-rev", "", "exact target revision (precondition)")
		reviewer := fs.String("reviewer", "", "claude|codex (confirmation is reviewer-judged)")
		model := fs.String("model", "", "explicit model")
		effort := fs.String("effort", "", "explicit effort")
		handles := fs.String("handles", "", "session handle store path (default: ~/.acrelay/handles.json, fail-closed)")
		workdir := fs.String("workdir", "", "reviewer cwd (default: objective trust profile mode)")
		fs.Parse(args)
		split := func(v string) []string {
			if strings.TrimSpace(v) == "" {
				return nil
			}
			parts := strings.Split(v, ",")
			for i := range parts {
				parts[i] = strings.TrimSpace(parts[i])
			}
			return parts
		}
		switch {
		case *open != "":
			st, err := relay.OpenConfirmation(*canonical, *round, split(*open))
			if err != nil {
				fail(err)
			}
			fmt.Printf("confirmation cycle open R%d: %s\n", *round, *open)
			_ = st
		case *submit != "":
			if *reviewer == "" {
				fail(fmt.Errorf("confirm -submit requires -reviewer: confirmation is judged by the reviewer, not entered manually"))
			}
			a, err := adapterFor(*reviewer)
			if err != nil {
				fail(err)
			}
			handlesPath, err := resolveHandles(*handles)
			if err != nil {
				fail(err)
			}
			cSess := &relay.Session{Adapter: a, Handles: &adapter.HandleStore{Path: handlesPath}, Canonical: *canonical}
			dispatchCtx, cancelDispatch := newDispatchContext()
			defer cancelDispatch()
			st, done, err := cSess.ConfirmWithReviewer(dispatchCtx, *round, *expected, split(*submit), *delta,
				adapter.Request{Model: *model, Effort: *effort, WorkingDir: *workdir})
			if err != nil {
				fail(err)
			}
			for _, c := range st.Confirmations {
				if c.RoundIndex == *round {
					fmt.Printf("R%d: valid_attempts=%d precondition_failures=%d outstanding=%v escalated=%v done=%v\n",
						*round, c.ValidAttempts, c.PreconditionFailures, c.Outstanding, c.Escalated, done)
				}
			}
		default:
			fail(fmt.Errorf("confirm requires -open or -submit"))
		}

	case "reconcile":
		fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		transaction := fs.String("transaction", "", "dispatch journal or legacy recovery transaction path")
		recovery := fs.String("recovery", "", "legacy alias for -transaction")
		fs.Parse(args)
		path := *transaction
		if path == "" {
			path = *recovery
		}
		if *canonical == "" || path == "" {
			fail(fmt.Errorf("reconcile requires -canonical and -transaction"))
		}
		st, err := relay.Reconcile(*canonical, path)
		if err != nil {
			fail(err)
		}
		fmt.Printf("reconciled: rounds=%d governance=%s\n", len(st.Rounds), st.Governance)

	case "abandon-transaction":
		fs := flag.NewFlagSet("abandon-transaction", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		transaction := fs.String("transaction", "", "un-reconcilable dispatch journal path")
		actor := fs.String("actor", "", "declared owner/arbiter identity")
		role := fs.String("role", "owner", "owner|arbiter")
		reason := fs.String("reason", "", "why normal reconcile is impossible")
		fs.Parse(args)
		if *canonical == "" || *transaction == "" {
			fail(fmt.Errorf("abandon-transaction requires -canonical and -transaction"))
		}
		st, quarantine, err := relay.AbandonTransaction(*canonical, *transaction, *actor, *role, *reason)
		if err != nil {
			fail(err)
		}
		fmt.Printf("transaction abandoned: objective=%s quarantine=%s\n", st.ObjectiveID, quarantine)

	case "cleanup":
		fs := flag.NewFlagSet("cleanup", flag.ExitOnError)
		canonical := fs.String("canonical", "", "exact owner-retained canonical record path")
		ref := fs.String("ref", "", "exact canonical-bound session_ref")
		quarantine := fs.String("quarantine", "", "exact canonical-bound quarantine sidecar path")
		orphanSidecar := fs.String("orphan-sidecar", "", "exact sidecar path whose declared canonical is missing")
		mode := fs.String("mode", "list", "list|dry-run|apply")
		handles := fs.String("handles", "", "session handle store path (default: ~/.acrelay/handles.json, fail-closed)")
		actor := fs.String("actor", "", "declared owner/arbiter applying cleanup")
		reason := fs.String("continuity-abandon-reason", "", "non-empty declaration that related-objective session continuity is abandoned")
		sidecarReason := fs.String("sidecar-reason", "", "non-empty owner reason for quarantine/orphan sidecar cleanup")
		fs.Parse(args)
		selected := 0
		for _, value := range []string{*ref, *quarantine, *orphanSidecar} {
			if value != "" {
				selected++
			}
		}
		if selected != 1 {
			fail(fmt.Errorf("cleanup requires exactly one of -ref, -quarantine, or -orphan-sidecar"))
		}
		if *ref != "" {
			if *sidecarReason != "" {
				fail(fmt.Errorf("-sidecar-reason is only valid with -quarantine or -orphan-sidecar"))
			}
			handlesPath, err := resolveHandlesPath(*handles)
			if err != nil {
				fail(err)
			}
			report, err := relay.CleanupSession(relay.CleanupRequest{
				Canonical: *canonical, Ref: *ref, Mode: relay.CleanupMode(*mode), Actor: *actor, Reason: *reason,
			}, &adapter.HandleStore{Path: handlesPath})
			if err != nil {
				fail(err)
			}
			fmt.Print(relay.RenderCleanupReport(report))
			if relay.CleanupMode(*mode) == relay.CleanupApply && len(report.Blocked) > 0 {
				fail(fmt.Errorf("cleanup apply blocked; no handle/cwd deletion occurred"))
			}
		} else {
			if *reason != "" {
				fail(fmt.Errorf("use -sidecar-reason, not -continuity-abandon-reason, for sidecar cleanup"))
			}
			req := relay.SidecarCleanupRequest{Canonical: *canonical, Mode: relay.CleanupMode(*mode), Actor: *actor, Reason: *sidecarReason}
			var report *relay.SidecarCleanupReport
			var err error
			if *quarantine != "" {
				req.Path = *quarantine
				report, err = relay.CleanupQuarantine(req)
			} else {
				req.Path = *orphanSidecar
				report, err = relay.CleanupOrphanSidecar(req)
			}
			if err != nil {
				fail(err)
			}
			fmt.Print(relay.RenderSidecarCleanupReport(report))
			if relay.CleanupMode(*mode) == relay.CleanupApply && len(report.Blocked) > 0 {
				fail(fmt.Errorf("sidecar cleanup apply blocked; no deletion occurred"))
			}
		}

	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		handles := fs.String("handles", "", "session handle store path for lifecycle diagnostics (default: ~/.acrelay/handles.json)")
		fs.Parse(args)
		handlesPath, err := resolveHandlesPath(*handles)
		if err != nil {
			fail(err)
		}
		out, err := relay.StatusWithHandles(*canonical, &adapter.HandleStore{Path: handlesPath})
		if err != nil {
			fail(err)
		}
		fmt.Print(out)

	case "briefing":
		code, err := runBriefing(args, os.Stdout)
		if err != nil {
			fail(err)
		}
		if code != 0 {
			os.Exit(code)
		}

	case "version":
		if err := runVersion(args, os.Stdout); err != nil {
			fail(err)
		}

	default:
		fail(fmt.Errorf("unknown command %q", cmd))
	}
}
