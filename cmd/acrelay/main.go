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
		fmt.Fprintln(os.Stderr, `usage: acrelay <init|review|disposition|close|terminate|status> [flags]
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
		targetRev := fs.String("target-rev", "", "target manifest revision")
		prior := fs.String("prior", "", "prior objective ID (same-target follow-up)")
		diff := fs.String("material-diff", "", "material difference vs prior objective")
		seen := fs.Bool("seen-before", false, "target manifest was reviewed before")
		fs.Parse(args)
		if *canonical == "" || *question == "" || *targetRev == "" {
			fail(fmt.Errorf("init requires -canonical, -question, -target-rev"))
		}
		st, err := relay.Init(*canonical, *question, *targetRev, *prior, *diff, *seen)
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
