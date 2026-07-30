package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersionReleaseShort(t *testing.T) {
	oldVersion, oldCommit := releaseVersion, releaseCommit
	releaseVersion, releaseCommit = "v0.1.0-alpha.5", "abc123"
	t.Cleanup(func() {
		releaseVersion, releaseCommit = oldVersion, oldCommit
	})

	var out bytes.Buffer
	if err := runVersion([]string{"--short"}, &out); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "v0.1.0-alpha.5" {
		t.Fatalf("short version = %q", got)
	}
}

func TestRunVersionHumanIncludesProvenance(t *testing.T) {
	oldVersion, oldCommit := releaseVersion, releaseCommit
	releaseVersion, releaseCommit = "v0.1.0-alpha.5", "abc123"
	t.Cleanup(func() {
		releaseVersion, releaseCommit = oldVersion, oldCommit
	})

	var out bytes.Buffer
	if err := runVersion(nil, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"acRelay v0.1.0-alpha.5", "commit abc123", "go go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("version output %q missing %q", got, want)
		}
	}
}

func TestRunVersionRejectsArguments(t *testing.T) {
	if err := runVersion([]string{"extra"}, &bytes.Buffer{}); err == nil {
		t.Fatal("version accepted an unexpected argument")
	}
}

func TestBoundedUserTextNormalizesAndTruncatesRunes(t *testing.T) {
	if got := boundedUserText(" alpha\n beta ", 20); got != "alpha beta" {
		t.Fatalf("normalized text = %q", got)
	}
	if got := boundedUserText("가나다라마바사", 3); got != "가나다…" {
		t.Fatalf("rune-bounded text = %q", got)
	}
}
