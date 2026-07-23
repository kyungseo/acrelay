package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunIsolatedMainEnvironment(t *testing.T) {
	root := os.Getenv(testRootEnv)
	if root == "" {
		t.Fatal("TestMain did not publish the isolated test root")
	}
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		value := os.Getenv(key)
		if value == "" || !pathWithin(root, value) {
			t.Fatalf("%s=%q is outside isolated root %q", key, value, root)
		}
	}
	if os.Getenv("GIT_CONFIG_NOSYSTEM") != "1" || os.Getenv("GIT_TERMINAL_PROMPT") != "0" {
		t.Fatal("Git isolation flags are not fail-closed")
	}
	gitConfig := os.Getenv("GIT_CONFIG_GLOBAL")
	if !pathWithin(root, gitConfig) {
		t.Fatalf("GIT_CONFIG_GLOBAL=%q is outside isolated root", gitConfig)
	}
	if b, err := os.ReadFile(gitConfig); err != nil || len(b) != 0 {
		t.Fatalf("global Git config must be an empty regular file: bytes=%d err=%v", len(b), err)
	}
	sentinelDir := os.Getenv(sentinelDirEnv)
	firstPath := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	if firstPath != sentinelDir {
		t.Fatalf("sentinel directory must be first in PATH: got %q want %q", firstPath, sentinelDir)
	}
}

func TestProductionBarrierMatrix(t *testing.T) {
	tests := []struct {
		name   string
		before func(t *testing.T, dir string)
		after  func(t *testing.T, dir string)
		want   string
	}{
		{name: "unchanged"},
		{name: "create handles", after: writeHandles("new"), want: "created"},
		{name: "modify handles", before: writeHandles("old"), after: writeHandles("new-longer"), want: "modified"},
		{name: "remove handles", before: writeHandles("old"), after: removeEntry("handles.json"), want: "removed"},
		{name: "create top-level", after: writeEntry("unexpected", "secret-value"), want: "created"},
		{name: "remove top-level", before: writeEntry("unexpected", "secret-value"), after: removeEntry("unexpected"), want: "removed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), ".acrelay")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.before != nil {
				tc.before(t, dir)
			}
			barrier, err := NewProductionBarrier(dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.after != nil {
				tc.after(t, dir)
			}
			joined := strings.Join(barrier.Violations(), "\n")
			if tc.want == "" && joined != "" {
				t.Fatalf("unexpected violation: %s", joined)
			}
			if tc.want != "" && !strings.Contains(joined, tc.want) {
				t.Fatalf("violation %q does not contain %q", joined, tc.want)
			}
			if strings.Contains(joined, "secret-value") {
				t.Fatal("barrier diagnostic leaked raw content")
			}
		})
	}
}

func TestProductionBarrierDigestDetectsSameMetadataChange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".acrelay")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "handles.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := NewProductionBarrier(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(barrier.Violations(), "\n")
	if !strings.Contains(joined, "content") {
		t.Fatalf("same-metadata content change was not detected: %q", joined)
	}
}

func TestProductionBarrierDetectsDirectoryCreation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".acrelay")
	barrier, err := NewProductionBarrier(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(barrier.Violations(), "\n")
	if !strings.Contains(joined, "created production .acrelay directory") {
		t.Fatalf("directory creation was not detected: %q", joined)
	}
}

func TestVendorSentinelIsFailFast(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "sentinel.log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeVendorSentinels(dir); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sentinelLogEnv, logPath)
	cmd := exec.Command(filepath.Join(dir, "claude"), "--version")
	err := cmd.Run()
	if err == nil {
		t.Fatal("sentinel unexpectedly succeeded")
	}
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 97 {
		t.Fatalf("sentinel exit=%v, want 97", err)
	}
	if b, err := os.ReadFile(logPath); err != nil || strings.TrimSpace(string(b)) != "claude" {
		t.Fatalf("sentinel audit log=%q err=%v", b, err)
	}
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func writeHandles(content string) func(*testing.T, string) {
	return writeEntry("handles.json", content)
}

func writeEntry(name, content string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func removeEntry(name string) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
}
