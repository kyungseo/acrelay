package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendHappyPathAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "canonical.md")
	raw := []byte("BEFORE\n```json\n{\"한글키\": \"값 with ```backticks``` and ~~~~ tildes\"}\n```\nAFTER")

	rev, err := Revision(path)
	if err != nil {
		t.Fatal(err)
	}
	section := "\n## R0 A0\n" + EncodeBlock("raw_stdout", raw)
	if _, err := AppendAtomic(path, section, rev); err != nil {
		t.Fatal(err)
	}
	stored, _ := os.ReadFile(path)
	got, err := ExtractBlock(string(stored), "raw_stdout")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("raw round-trip mismatch")
	}
}

// mid-dispatch external edit: snapshot -> edit -> append must fail closed.
func TestRevisionConflictFailClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "canonical.md")
	if err := os.WriteFile(path, []byte("# c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, _ := Revision(path)
	// external edit during (simulated) model execution
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("<!-- concurrent edit -->\n")
	f.Close()
	_, err := AppendAtomic(path, "\nshould-not-append\n", snap)
	if err == nil || !strings.Contains(err.Error(), "revision-conflict") {
		t.Fatalf("expected revision-conflict fail-closed, got %v", err)
	}
	stored, _ := os.ReadFile(path)
	if strings.Contains(string(stored), "should-not-append") {
		t.Fatal("conflicting append must not be written")
	}
}

// fence collision: content containing long tilde runs must still round-trip.
func TestFenceCollision(t *testing.T) {
	raw := []byte("text with\n~~~~~~~~\nlong tilde fence inside")
	block := EncodeBlock("raw_stdout", raw)
	got, err := ExtractBlock(block, "raw_stdout")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("fence-collision round-trip mismatch")
	}
}

// invalid UTF-8 must take the base64 path and round-trip byte-exactly.
func TestInvalidUTF8Base64RoundTrip(t *testing.T) {
	raw := []byte{0xff, 0xfe, 0x00, 0x41, 0x80, 0x81}
	block := EncodeBlock("raw_stderr", raw)
	if !strings.Contains(block, "encoding=base64") {
		t.Fatal("invalid UTF-8 must use base64 encoding")
	}
	got, err := ExtractBlock(block, "raw_stderr")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatal("base64 round-trip mismatch")
	}
}

// tampered stored content must fail the digest re-check.
func TestTamperedBlockFailsClosed(t *testing.T) {
	raw := []byte("original content")
	block := EncodeBlock("raw_stdout", raw)
	tampered := strings.Replace(block, "original", "tampered", 1)
	if _, err := ExtractBlock(tampered, "raw_stdout"); err == nil {
		t.Fatal("tampered block must fail digest verification")
	}
}

func TestFirstAppendOnMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.md")
	rev, _ := Revision(path) // empty digest
	if _, err := AppendAtomic(path, "# Canonical\n", rev); err != nil {
		t.Fatal(err)
	}
}

func TestNoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "canonical.md")
	rev, _ := Revision(path)
	if _, err := AppendAtomic(path, "x\n", rev); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
