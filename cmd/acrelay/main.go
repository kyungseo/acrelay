// Command acrelay is the v1 one-shot review relay CLI (working name).
// It automates the reviewer leg only: request assembly, one child
// invocation, capture, validation, and canonical-record append. Driver
// dispositions and arbiter decisions stay human commands.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/relay"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/subject"
)

func defaultHandles() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".acrelay-handles.json"
	}
	dir := filepath.Join(home, ".acrelay")
	os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "handles.json")
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: acrelay <init|review|confirm|disposition|advance|close|terminate|reconcile|abandon-transaction|status> [flags]
The canonical record is private local storage: keep it outside shared/synced/
published paths. Sharing requires a redacted export (not provided in v1).`)
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
		st, err := relay.InitSubject(*canonical, *question, spec, *prior, *diff, *seen, policy)
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
		handles := fs.String("handles", defaultHandles(), "session handle store path")
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
		s := &relay.Session{
			Adapter: a, Handles: &adapter.HandleStore{Path: *handles}, Canonical: *canonical,
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
		st, outcome, err := s.Review(context.Background(), p,
			adapter.Request{Model: *model, Effort: *effort, WorkingDir: *workdir})
		if err != nil {
			fail(err) // the "failed"/"unknown" progress line was already emitted
		}
		fmt.Fprintf(os.Stderr, "progress: terminal session_ref=%s\n", st.SessionRef)
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
		arbiter := fs.String("arbiter", "", "arbiter identity (needs-user)")
		reason := fs.String("reason", "", "arbiter reason (needs-user)")
		fs.Parse(args)
		var ad *review.ArbiterDecision
		if *arbiter != "" || *reason != "" {
			ad = &review.ArbiterDecision{Arbiter: *arbiter, Reason: *reason}
		}
		st, err := relay.Disposition(*canonical, *finding, review.Disposition(*decision), ad)
		if err != nil {
			fail(err)
		}
		fmt.Printf("finding %s -> %s (governance=%s)\n", *finding, *decision, st.Governance)

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
		handles := fs.String("handles", defaultHandles(), "session handle store path")
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
			cSess := &relay.Session{Adapter: a, Handles: &adapter.HandleStore{Path: *handles}, Canonical: *canonical}
			st, done, err := cSess.ConfirmWithReviewer(context.Background(), *round, *expected, split(*submit), *delta,
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

	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		canonical := fs.String("canonical", "", "canonical record path")
		fs.Parse(args)
		out, err := relay.Status(*canonical)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)

	default:
		fail(fmt.Errorf("unknown command %q", cmd))
	}
}
