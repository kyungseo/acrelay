package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
