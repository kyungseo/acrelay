//go:build darwin || linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/testenv"
)

// R2-CX-F3 item 2/3: a real acrelay `review` subprocess, dispatching to a
// hanging fake vendor, receives an actual SIGINT. The production
// newDispatchContext must cancel the in-flight dispatch and the canonical must
// record an UNKNOWN round with the canceled.parent-signal cause — proving the
// real main wiring, ErrParentSignal path, and durable classification.
func TestRealCLIParentSignalRecordsUnknown(t *testing.T) {
	host := runtime.GOOS + "/" + runtime.GOARCH
	verified := false
	for _, p := range (adapter.ClaudeAdapter{}).Capability().KnownGoodPlatforms {
		if p == host {
			verified = true
		}
	}
	if !verified {
		t.Skipf("capability: real-adapter dispatch is restriction-gated on %s (verified platforms only)", host)
	}
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
	testenv.InstallFakeVendor(t, filepath.Join(vendorDir, "claude"), claude)

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
	var reviewOutput bytes.Buffer
	review.Stdout, review.Stderr = &reviewOutput, &reviewOutput
	if err := review.Start(); err != nil {
		t.Fatal(err)
	}
	// Wait until the child vendor is actually running, then deliver SIGINT.
	waitForFile(t, filepath.Join(dir, "child-started"), 10*time.Second, reviewOutput.String)
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
