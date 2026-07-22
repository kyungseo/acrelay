package testenv

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Dialect-engine table tests (R1-CX-F5): the fakevendor helper is itself
// under test — transcripts (stdout/stderr/exit) are asserted exactly so the
// dialect cannot silently diverge from the fixture text it replaces.

func runFake(t *testing.T, fixture string, env []string, args ...string) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "vendor-under-test")
	InstallFakeVendor(t, bin, fixture)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return stdout.String(), stderr.String(), code
}

func TestFakeVendorDialectTranscripts(t *testing.T) {
	claudeFixture := `if [ "$1" = "--version" ]; then echo "2.1.217 (Claude Code)"; exit 0; fi
if [ "$1" = "--help" ]; then echo '--output-format --json-schema'; exit 0; fi
for a in "$@"; do
  if [ "$a" = "--resume" ]; then
    printf '%s\n' '{"type":"result","is_error":true}'
    exit 1
  fi
done
printf '%s\n' '{"type":"result","subtype":"success"}'
`
	t.Run("positional condition", func(t *testing.T) {
		out, _, code := runFake(t, claudeFixture, nil, "--version")
		if out != "2.1.217 (Claude Code)\n" || code != 0 {
			t.Fatalf("version transcript mismatch: %q code=%d", out, code)
		}
	})
	t.Run("arg scan branch", func(t *testing.T) {
		out, _, code := runFake(t, claudeFixture, nil, "-p", "--resume", "x")
		if out != "{\"type\":\"result\",\"is_error\":true}\n" || code != 1 {
			t.Fatalf("resume transcript mismatch: %q code=%d", out, code)
		}
	})
	t.Run("fallthrough envelope", func(t *testing.T) {
		out, _, code := runFake(t, claudeFixture, nil, "-p")
		if out != "{\"type\":\"result\",\"subtype\":\"success\"}\n" || code != 0 {
			t.Fatalf("envelope transcript mismatch: %q code=%d", out, code)
		}
	})
	t.Run("or-condition and stderr", func(t *testing.T) {
		fixture := `if [ "$1" = "-v" ] || [ "$1" = "--version" ]; then echo 'warn' >&2; echo 'codex-cli 0.144.1'; exit 0; fi
exit 9
`
		out, errOut, code := runFake(t, fixture, nil, "-v")
		if out != "codex-cli 0.144.1\n" || errOut != "warn\n" || code != 0 {
			t.Fatalf("or/stderr mismatch: out=%q err=%q code=%d", out, errOut, code)
		}
		if _, _, code := runFake(t, fixture, nil, "other"); code != 9 {
			t.Fatalf("exit code path mismatch: %d", code)
		}
	})
	t.Run("echo append env target", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "calls.log")
		fixture := "echo \"$@\" >> \"$ACRELAY_TEST_LOG\"\nexit 0\n"
		_, _, code := runFake(t, fixture, []string{"ACRELAY_TEST_LOG=" + logPath}, "a", "b c")
		if code != 0 {
			t.Fatalf("append exit=%d", code)
		}
		b, err := os.ReadFile(logPath)
		if err != nil || string(b) != "a b c\n" {
			t.Fatalf("append transcript mismatch: %q err=%v", b, err)
		}
	})
	t.Run("printf without newline", func(t *testing.T) {
		out, _, _ := runFake(t, "printf '%s' 'raw'\n", nil)
		if out != "raw" {
			t.Fatalf("printf %%s mismatch: %q", out)
		}
	})
	t.Run("sleep and touch", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "m")
		start := time.Now()
		_, _, code := runFake(t, "sleep 0.2\ntouch \""+marker+"\"\n", nil)
		if code != 0 || time.Since(start) < 200*time.Millisecond {
			t.Fatalf("sleep not honored: code=%d elapsed=%v", code, time.Since(start))
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatal("touch marker missing")
		}
	})
	t.Run("spawn records grandchild", func(t *testing.T) {
		dir := t.TempDir()
		bin := filepath.Join(dir, "spawner")
		InstallFakeVendor(t, bin, "spawn 30\nexit 0\n")
		exe := bin
		if runtime.GOOS == "windows" {
			exe += ".exe"
		}
		if err := exec.Command(exe).Run(); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(exe + ".grandchild")
		if err != nil {
			t.Fatal("grandchild pid file missing")
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 {
			t.Fatalf("grandchild pid invalid: %q", b)
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
	t.Run("self kill posix", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("capability: POSIX-only fixture construct")
		}
		_, _, code := runFake(t, "kill -KILL $$\n", nil)
		if code != -1 { // signal death has no exit code
			t.Fatalf("self-kill must die by signal, exit=%d", code)
		}
	})
}

// The lint gate: unsupported constructs — even in branches execution would
// not take — are rejected before installation, and the engine's runtime
// diagnostic stays loud (exit 97).
func TestFakeVendorLintRejectsUnsupportedConstructs(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "vendor-lint")
	InstallFakeVendor(t, bin, "exit 0\n") // builds the helper + valid fixture
	exe := bin
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	bad := filepath.Join(dir, "bad.fixture")
	if err := os.WriteFile(bad, []byte("if [ \"$1\" = \"x\" ]; then frobnicate; fi\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd := exec.Command(exe, "--fakevendor-lint", bad)
	cmd.Stderr = &stderr
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 97 || !strings.Contains(stderr.String(), "unsupported fixture construct") {
		t.Fatalf("lint must reject dead-branch unsupported syntax with exit 97: err=%v stderr=%q", err, stderr.String())
	}
	// Runtime diagnostic path: overwrite the installed fixture after lint to
	// simulate an unlinted construct reaching execution.
	if err := os.WriteFile(exe+".fixture", []byte("frobnicate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr2 bytes.Buffer
	run := exec.Command(exe)
	run.Stderr = &stderr2
	err = run.Run()
	ee, ok = err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 97 || !strings.Contains(stderr2.String(), "unsupported fixture construct") {
		t.Fatalf("runtime unsupported construct must be loud: err=%v stderr=%q", err, stderr2.String())
	}
}
