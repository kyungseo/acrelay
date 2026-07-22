package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/relay"
	"github.com/kyungseo/acrelay/internal/subject"
)

func TestParseOptionalFormalRoundBound(t *testing.T) {
	tests := []struct {
		raw  string
		want int
		ok   bool
	}{
		{raw: "", want: 0, ok: true},
		{raw: "1", want: 1, ok: true},
		{raw: "3", want: 3, ok: true},
		{raw: "5", want: 5, ok: true},
		{raw: "0", ok: false},
		{raw: "6", ok: false},
		{raw: "nope", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, err := parseOptionalFormalRoundBound(tt.raw)
			if tt.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("expected validation error")
			}
			if tt.ok && got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseSubjectInput(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := parseSubjectInput(target, "")
	if err != nil || spec.Kind != subject.KindFile {
		t.Fatalf("single-file shorthand = %+v, %v", spec, err)
	}

	specPath := filepath.Join(root, "subject.json")
	if err := os.WriteFile(specPath, []byte(`{"kind":"subtree","root":"."}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err = parseSubjectInput("", specPath)
	if err != nil || spec.Kind != subject.KindSubtree || spec.Root != root {
		t.Fatalf("descriptor = %+v, %v", spec, err)
	}
	if _, err := parseSubjectInput("", ""); err == nil {
		t.Fatal("missing selector accepted")
	}
	if _, err := parseSubjectInput(target, specPath); err == nil {
		t.Fatal("ambiguous selector accepted")
	}
}

func TestRunBriefingHumanJSONAndCheckExit(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	canonical := filepath.Join(dir, "canonical.md")
	if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := adapter.NewTrustPolicy("owner", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Init(canonical, "is this ready?", target, "", "", false, policy); err != nil {
		t.Fatal(err)
	}

	var human bytes.Buffer
	code, err := runBriefing([]string{"-canonical", canonical}, &human)
	if err != nil || code != 0 || !strings.Contains(human.String(), "readiness: blocked") {
		t.Fatalf("default human briefing failed: code=%d err=%v output=%q", code, err, human.String())
	}

	var machine bytes.Buffer
	code, err = runBriefing([]string{"-canonical", canonical, "-format", "json", "-check"}, &machine)
	if err != nil || code != relay.BriefingCheckBlocked {
		t.Fatalf("checked JSON briefing failed: code=%d err=%v", code, err)
	}
	var parsed relay.Briefing
	if err := json.Unmarshal(machine.Bytes(), &parsed); err != nil {
		t.Fatalf("machine briefing is not JSON: %v\n%s", err, machine.String())
	}
	if parsed.Version != relay.BriefingOutputVersion || parsed.Readiness != relay.BriefingBlocked {
		t.Fatalf("machine briefing contract mismatch: %+v", parsed)
	}
	if _, err := runBriefing([]string{"-canonical", canonical, "-format", "yaml"}, &bytes.Buffer{}); err == nil {
		t.Fatal("unsupported briefing format must fail")
	}
}

// FEAT-20260721-002 R0-CX-F5: first signal cancels the dispatch context with
// the parent-signal cause; the second escalates to force-exit.
func TestDispatchSignalContextTwoStagePolicy(t *testing.T) {
	signals := make(chan os.Signal, 2)
	forced := make(chan struct{})
	ctx, cancel := dispatchSignalContext(context.Background(), signals, func() { close(forced) })
	defer cancel()

	signals <- syscall.SIGINT
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first signal must cancel the dispatch context")
	}
	if !errors.Is(context.Cause(ctx), adapter.ErrParentSignal) {
		t.Fatalf("cancellation cause = %v, want ErrParentSignal", context.Cause(ctx))
	}
	select {
	case <-forced:
		t.Fatal("force exit must not fire on the first signal")
	default:
	}
	signals <- syscall.SIGTERM
	select {
	case <-forced:
	case <-time.After(2 * time.Second):
		t.Fatal("second signal must trigger the force-exit path")
	}
}

// A normal completion (cancel(nil)) must not leave the goroutine treating
// teardown as a signal.
func TestDispatchSignalContextNormalCompletion(t *testing.T) {
	signals := make(chan os.Signal, 2)
	forced := make(chan struct{})
	_, cancel := dispatchSignalContext(context.Background(), signals, func() { close(forced) })
	cancel()
	select {
	case <-forced:
		t.Fatal("normal completion must never force-exit")
	case <-time.After(100 * time.Millisecond):
	}
}

// R2-CX-F3 item 2/3: a real acrelay `review` subprocess, dispatching to a
// hanging fake vendor, receives an actual SIGINT. The production
// newDispatchContext must cancel the in-flight dispatch and the canonical must
// record an UNKNOWN round with the canceled.parent-signal cause — proving the
// real main wiring, ErrParentSignal path, and durable classification.
func TestRealCLIParentSignalRecordsUnknown(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "acrelay")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = mustModuleDir(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build acrelay: %v\n%s", err, out)
	}

	// A fake `claude` that starts, emits nothing terminal, and hangs — so the
	// dispatch is in-flight when the SIGINT arrives.
	vendorDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	claude := `#!/bin/sh
if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema --resume --safe-mode --add-dir --tools --permission-mode --system-prompt'; exit 0; fi
touch "` + filepath.Join(dir, "child-started") + `"
sleep 60
`
	if err := os.WriteFile(filepath.Join(vendorDir, "claude"), []byte(claude), 0o755); err != nil {
		t.Fatal(err)
	}

	canonical := filepath.Join(dir, "review.md")
	target := filepath.Join(dir, "target.go")
	handles := filepath.Join(dir, "handles.json")
	if err := os.WriteFile(target, []byte(`func greet() string { return "hi" }`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+vendorDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	initCmd := exec.Command(bin, "init", "-canonical", canonical, "-question", "ready?",
		"-target", target, "-approval-actor", "owner", "-ack-vendor-egress")
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	review := exec.Command(bin, "review", "-canonical", canonical, "-reviewer", "claude",
		"-prompt", "look", "-handles", handles)
	review.Env = env
	review.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := review.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait until the child vendor is actually running, then deliver SIGINT.
	waitForFile(t, filepath.Join(dir, "child-started"), 10*time.Second)
	if err := review.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	_ = review.Wait() // review exits nonzero after the cancellation

	// The canonical must record an UNKNOWN round carrying the parent-signal
	// cause (reconciled from the durable journal or recorded inline).
	status := exec.Command(bin, "status", "-canonical", canonical)
	status.Env = env
	out, _ := status.CombinedOutput()
	body, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "UNKNOWN") {
		t.Fatalf("canonical must record an UNKNOWN round after SIGINT.\nstatus:\n%s\ncanonical:\n%s", out, body)
	}
	if !strings.Contains(string(body), adapter.CauseCanceledParentSignal) &&
		!strings.Contains(string(body), adapter.CauseJournalReconciledUnknown) {
		t.Fatalf("UNKNOWN round must carry the parent-signal (or reconciled-unknown) cause.\ncanonical:\n%s", body)
	}
}

// FEAT-20260722-001 AR-3: requesting the host-subagent execution surface at
// init fails closed through the real CLI with the actionable diagnostic, and
// no canonical is created — no silent fallback to another topology.
func TestRealCLIInitHostSubagentFailsClosed(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "acrelay")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Dir = mustModuleDir(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build acrelay: %v\n%s", err, out)
	}
	canonical := filepath.Join(dir, "review.md")
	target := filepath.Join(dir, "target.go")
	if err := os.WriteFile(target, []byte(`func greet() string { return "hi" }`), 0o600); err != nil {
		t.Fatal(err)
	}
	initCmd := exec.Command(bin, "init", "-canonical", canonical, "-question", "ready?",
		"-target", target, "-approval-actor", "owner", "-ack-vendor-egress",
		"-execution-surface", "host-subagent")
	out, err := initCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("host-subagent init must fail closed:\n%s", out)
	}
	for _, needle := range []string{"not supported", "deferred", "no fallback"} {
		if !strings.Contains(string(out), needle) {
			t.Fatalf("diagnostic must be actionable (missing %q):\n%s", needle, out)
		}
	}
	if _, statErr := os.Stat(canonical); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed init must not create a canonical: %v", statErr)
	}
}

func mustModuleDir(t *testing.T) string {
	t.Helper()
	// cmd/acrelay -> module root is two levels up.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd) // test runs in cmd/acrelay; `go build .` builds this package
}

func waitForFile(t *testing.T, path string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
