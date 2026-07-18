// Package store implements the canonical Markdown artifact (DR-811 §2):
// pre-dispatch revision snapshots, relay-managed raw append with
// content-derived fences (base64 for invalid UTF-8), unique-temp atomic
// replace, and fail-closed conflict detection.
//
// The canonical file lives in private storage outside the host's
// sharing/sync/publish boundary; callers own placement. Sharing happens only
// through a noncanonical redacted export, which this package does not
// implement in v1.
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Digest returns the hex sha256 of raw bytes with no canonicalization.
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Revision snapshots the canonical file's current revision. A missing file
// snapshots the empty digest so first-append works under the same contract.
func Revision(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Digest(nil), nil
		}
		return "", err
	}
	return Digest(b), nil
}

// fenceFor computes a tilde fence strictly longer than any tilde run in the
// content (minimum 4). The closing fence carries fence characters only.
func fenceFor(text string) string {
	max := 3
	for _, run := range regexp.MustCompile(`~+`).FindAllString(text, -1) {
		if len(run) > max {
			max = len(run)
		}
	}
	return strings.Repeat("~", max+1)
}

// EncodeBlock renders one raw byte stream as a labeled evidence block.
// Valid UTF-8 is stored verbatim inside a content-derived fence; anything
// else is stored as base64. The digest always covers the original bytes.
func EncodeBlock(label string, b []byte) string {
	if utf8.Valid(b) {
		f := fenceFor(string(b))
		return fmt.Sprintf("- %s: encoding=utf-8 sha256=%s bytes=%d\n%s\n%s\n%s\n",
			label, Digest(b), len(b), f, string(b), f)
	}
	enc := base64.StdEncoding.EncodeToString(b)
	f := fenceFor(enc)
	return fmt.Sprintf("- %s: encoding=base64 sha256=%s bytes=%d\n%s\n%s\n%s\n",
		label, Digest(b), len(b), f, enc, f)
}

// ExtractBlock re-reads a stored block and returns the original bytes after
// verifying the recomputed digest against the recorded one. Any mismatch or
// malformed block fails closed.
func ExtractBlock(stored, label string) ([]byte, error) {
	// Go regexp (RE2) has no backreferences, so the closing fence is located
	// by exact string search for the opening fence. Because the fence is
	// strictly longer than any run inside the content, "\n<fence>\n" cannot
	// occur within the content itself.
	head := regexp.MustCompile(`- ` + regexp.QuoteMeta(label) +
		`: encoding=(utf-8|base64) sha256=([0-9a-f]{64}) bytes=\d+\n(~{4,})\n`)
	all := head.FindAllStringSubmatchIndex(stored, -1)
	if len(all) == 0 {
		return nil, fmt.Errorf("block %q not found or malformed: fail-closed", label)
	}
	if len(all) > 1 {
		return nil, fmt.Errorf("block %q is ambiguous (%d occurrences — labels must be unique per scope, forged headers fail closed)", label, len(all))
	}
	m := all[0]
	encoding := stored[m[2]:m[3]]
	claimed := stored[m[4]:m[5]]
	fence := stored[m[6]:m[7]]
	rest := stored[m[1]:]
	end := strings.Index(rest, "\n"+fence+"\n")
	if end < 0 {
		return nil, fmt.Errorf("block %q closing fence missing: fail-closed", label)
	}
	body := rest[:end]
	var raw []byte
	if encoding == "base64" {
		dec, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, fmt.Errorf("block %q base64 decode: %w", label, err)
		}
		raw = dec
	} else {
		raw = []byte(body)
	}
	if Digest(raw) != claimed {
		return nil, fmt.Errorf("block %q digest mismatch (stored %s, recomputed %s): fail-closed",
			label, claimed[:12], Digest(raw)[:12])
	}
	return raw, nil
}

// AppendAtomic appends a section iff the canonical file still matches the
// pre-dispatch expectedRev. On any drift it fails closed without writing.
// The write path is unique-temp (same directory) -> fsync -> rename.
var hexRev = regexp.MustCompile(`^[0-9a-f]{64}$`)

func AppendAtomic(path, section, expectedRev string) (newRev string, err error) {
	if !hexRev.MatchString(expectedRev) {
		return "", fmt.Errorf("expected revision %q is not a sha256 hex digest: fail-closed", expectedRev)
	}
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if Digest(current) != expectedRev {
		return "", fmt.Errorf("revision-conflict: canonical changed since pre-dispatch snapshot (expected %s, found %s): fail-closed",
			expectedRev[:12], Digest(current)[:12])
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	tmp := filepath.Join(filepath.Dir(path),
		fmt.Sprintf(".%s.tmp-%d-%s", filepath.Base(path), os.Getpid(), hex.EncodeToString(suffix)))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp)
	next := append(current, []byte(section)...)
	if _, err := f.Write(next); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return Digest(next), nil
}
