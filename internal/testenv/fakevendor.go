package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// InstallFakeVendor installs the portable fake vendor CLI at path (adding
// .exe on Windows) and stores the fixture text next to it
// (FEAT-20260722-002 R0-CX-F8). Every platform executes the same fixture
// text through the fakevendor dialect engine — /bin/sh is no longer used, so
// the fixtures run natively on Windows lanes too.
func InstallFakeVendor(t *testing.T, path, fixture string) {
	t.Helper()
	bin, err := fakeVendorBinary()
	if err != nil {
		t.Fatalf("build fakevendor helper: %v", err)
	}
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	raw, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".fixture", []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	// R1-CX-F5: validate the ENTIRE fixture (every branch) at install time so
	// a dead branch with unsupported dialect syntax fails the test here — a
	// runtime exit-97 can then never masquerade as an intended vendor failure
	// for constructs the fixture actually contains.
	lint := exec.Command(bin, "--fakevendor-lint", path+".fixture")
	if out, err := lint.CombinedOutput(); err != nil {
		t.Fatalf("fixture failed dialect lint: %v\n%s\nfixture:\n%s", err, out, fixture)
	}
}

var (
	fakeVendorOnce sync.Once
	fakeVendorPath string
	fakeVendorErr  error
)

// fakeVendorBinary builds the helper once per test-binary run.
func fakeVendorBinary() (string, error) {
	fakeVendorOnce.Do(func() {
		dir, err := os.MkdirTemp("", "acrelay-fakevendor-")
		if err != nil {
			fakeVendorErr = err
			return
		}
		out := filepath.Join(dir, "fakevendor")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out,
			"github.com/kyungseo/acrelay/internal/testenv/fakevendor")
		if msg, err := cmd.CombinedOutput(); err != nil {
			fakeVendorErr = &buildError{output: string(msg), err: err}
			return
		}
		fakeVendorPath = out
	})
	return fakeVendorPath, fakeVendorErr
}

type buildError struct {
	output string
	err    error
}

func (b *buildError) Error() string { return b.err.Error() + ": " + b.output }
