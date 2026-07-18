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
	"strings"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/relay"
	"github.com/kyungseo/acrelay/internal/review"
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

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, `usage: acrelay <init|review|confirm|disposition|advance|close|terminate|reconcile|status> [flags]
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
		target := fs.String("target", "", "target file (evidence pointer: location + raw digest)")
		prior := fs.String("prior", "", "prior objective ID (same-target follow-up)")
		diff := fs.String("material-diff", "", "material difference vs prior objective")
		seen := fs.Bool("seen-before", false, "target manifest was reviewed before")
		fs.Parse(args)
		if *canonical == "" || *question == "" || *target == "" {
			fail(fmt.Errorf("init requires -canonical, -question, -target"))
		}
		st, err := relay.Init(*canonical, *question, *target, *prior, *diff, *seen)
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
		handles := fs.String("handles", defaultHandles(), "session handle store path")
		workdir := fs.String("workdir", "", "reviewer working directory (default: current)")
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
		s := &relay.Session{Adapter: a, Handles: &adapter.HandleStore{Path: *handles}, Canonical: *canonical}
		if *resetMode != "" || *resetReason != "" {
			s.Reset = &relay.SessionReset{Mode: *resetMode, Reason: *resetReason}
		}
		fmt.Fprintf(os.Stderr, "progress: started reviewer=%s\n", *reviewer)
		st, outcome, err := s.Review(context.Background(), p,
			adapter.Request{Model: *model, Effort: *effort, WorkingDir: *workdir})
		if err != nil {
			fail(err)
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
		fs.Parse(args)
		st, err := relay.Close(*canonical)
		if err != nil {
			fail(err)
		}
		fmt.Printf("objective %s CLOSED\n", st.ObjectiveID)

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
				adapter.Request{Model: *model, Effort: *effort})
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
		recovery := fs.String("recovery", "", "recovery transaction path")
		fs.Parse(args)
		st, err := relay.Reconcile(*canonical, *recovery)
		if err != nil {
			fail(err)
		}
		fmt.Printf("reconciled: rounds=%d governance=%s\n", len(st.Rounds), st.Governance)

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
