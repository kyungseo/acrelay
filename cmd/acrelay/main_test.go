package main

import (
	"os"
	"path/filepath"
	"testing"

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
