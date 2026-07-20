// Package testenv owns deterministic process-level isolation for acrelay tests.
// It is imported only by test binaries through package TestMain functions.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	testRootEnv    = "ACRELAY_TEST_ROOT"
	sentinelDirEnv = "ACRELAY_TEST_SENTINEL_DIR"
	sentinelLogEnv = "ACRELAY_TEST_SENTINEL_LOG"
)

var isolatedEnvKeys = []string{
	"HOME",
	"USERPROFILE",
	"HOMEDRIVE",
	"HOMEPATH",
	"XDG_CONFIG_HOME",
	"CLAUDE_CONFIG_DIR",
	"CODEX_HOME",
	"GIT_CONFIG_GLOBAL",
	"GIT_CONFIG_NOSYSTEM",
	"GIT_TERMINAL_PROMPT",
	"PATH",
	"TMPDIR",
	"TMP",
	"TEMP",
	testRootEnv,
	sentinelDirEnv,
	sentinelLogEnv,
}

// RunIsolatedMain wraps a risky package's TestMain. It snapshots the real
// default data footprint before redirecting home/config state, installs
// fail-fast vendor CLI sentinels, and fails if either sentinel execution or a
// production-data mutation is observed.
func RunIsolatedMain(m *testing.M) int {
	prodDir, err := defaultProductionDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "acrelay test isolation: resolve production dir: %v\n", err)
		return 1
	}
	barrier, err := NewProductionBarrier(prodDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acrelay test isolation: snapshot production dir: %v\n", err)
		return 1
	}

	root, err := os.MkdirTemp("", "acrelay-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "acrelay test isolation: create temp root: %v\n", err)
		return 1
	}
	defer os.RemoveAll(root)

	env, restore, err := configureIsolatedEnvironment(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acrelay test isolation: configure environment: %v\n", err)
		return 1
	}
	defer restore()

	code := m.Run()
	if b, err := os.ReadFile(env.SentinelLog); err != nil {
		fmt.Fprintf(os.Stderr, "acrelay test isolation: read sentinel log: %v\n", err)
		if code == 0 {
			code = 1
		}
	} else if strings.TrimSpace(string(b)) != "" {
		fmt.Fprintf(os.Stderr, "acrelay test isolation: blocked real vendor CLI resolution:\n%s", b)
		if code == 0 {
			code = 1
		}
	}
	if violations := barrier.Violations(); len(violations) != 0 {
		for _, violation := range violations {
			fmt.Fprintf(os.Stderr, "acrelay test isolation: %s\n", violation)
		}
		if code == 0 {
			code = 1
		}
	}
	return code
}

type isolatedEnvironment struct {
	Root            string
	Home            string
	SentinelDir     string
	SentinelLog     string
	GitGlobalConfig string
}

func defaultProductionDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".acrelay"), nil
}

func configureIsolatedEnvironment(root string) (*isolatedEnvironment, func(), error) {
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	claudeConfig := filepath.Join(root, "claude-config")
	codexHome := filepath.Join(root, "codex-home")
	tempRoot := filepath.Join(root, "tmp")
	sentinelDir := filepath.Join(root, "sentinel-bin")
	for _, dir := range []string{home, xdg, claudeConfig, codexHome, tempRoot, sentinelDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, func() {}, err
		}
	}

	gitConfig := filepath.Join(root, "gitconfig-global")
	if err := os.WriteFile(gitConfig, nil, 0o600); err != nil {
		return nil, func() {}, err
	}
	sentinelLog := filepath.Join(root, "sentinel.log")
	if err := os.WriteFile(sentinelLog, nil, 0o600); err != nil {
		return nil, func() {}, err
	}
	if err := writeVendorSentinels(sentinelDir); err != nil {
		return nil, func() {}, err
	}

	originalPath := os.Getenv("PATH")
	values := map[string]string{
		"HOME":                home,
		"USERPROFILE":         home,
		"HOMEDRIVE":           "",
		"HOMEPATH":            home,
		"XDG_CONFIG_HOME":     xdg,
		"CLAUDE_CONFIG_DIR":   claudeConfig,
		"CODEX_HOME":          codexHome,
		"GIT_CONFIG_GLOBAL":   gitConfig,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_TERMINAL_PROMPT": "0",
		"PATH":                sentinelDir + string(os.PathListSeparator) + originalPath,
		"TMPDIR":              tempRoot,
		"TMP":                 tempRoot,
		"TEMP":                tempRoot,
		testRootEnv:           root,
		sentinelDirEnv:        sentinelDir,
		sentinelLogEnv:        sentinelLog,
	}
	if runtime.GOOS == "windows" {
		volume := filepath.VolumeName(home)
		values["HOMEDRIVE"] = volume
		values["HOMEPATH"] = strings.TrimPrefix(home, volume)
	}
	restore, err := replaceEnvironment(values)
	if err != nil {
		return nil, func() {}, err
	}
	return &isolatedEnvironment{
		Root: root, Home: home, SentinelDir: sentinelDir,
		SentinelLog: sentinelLog, GitGlobalConfig: gitConfig,
	}, restore, nil
}

type savedEnv struct {
	value string
	set   bool
}

func replaceEnvironment(values map[string]string) (func(), error) {
	saved := make(map[string]savedEnv, len(isolatedEnvKeys))
	for _, key := range isolatedEnvKeys {
		value, set := os.LookupEnv(key)
		saved[key] = savedEnv{value: value, set: set}
	}
	for key, value := range values {
		if err := os.Setenv(key, value); err != nil {
			restoreEnvironment(saved)
			return func() {}, err
		}
	}
	return func() { restoreEnvironment(saved) }, nil
}

func restoreEnvironment(saved map[string]savedEnv) {
	for _, key := range isolatedEnvKeys {
		prior := saved[key]
		if prior.set {
			_ = os.Setenv(key, prior.value)
		} else {
			_ = os.Unsetenv(key)
		}
	}
}

func writeVendorSentinels(dir string) error {
	for _, vendor := range []string{"claude", "codex"} {
		path := filepath.Join(dir, vendor)
		body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %q >> \"${ACRELAY_TEST_SENTINEL_LOG:?}\"\nprintf 'acrelay test isolation: installed %s CLI is blocked\\n' >&2\nexit 97\n",
			vendor, vendor)
		if runtime.GOOS == "windows" {
			path += ".bat"
			body = fmt.Sprintf("@echo off\r\necho %s>>\"%%ACRELAY_TEST_SENTINEL_LOG%%\"\r\necho acrelay test isolation: installed %s CLI is blocked 1>&2\r\nexit /b 97\r\n",
				vendor, vendor)
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			return err
		}
	}
	return nil
}
