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

// ReadAll returns the whole canonical document ("" when the file does not
// exist yet).
func ReadAll(path string) (string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(b), nil
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

// Block is one relay-managed evidence block found OUTSIDE any fence.
type Block struct {
	Label    string
	Encoding string
	Claimed  string // recorded sha256
	Body     string // fenced body text (base64 text or verbatim UTF-8)
}

var blockHead = regexp.MustCompile(`^- (.+): encoding=(utf-8|base64) sha256=([0-9a-f]{64}) bytes=\d+$`)
var fenceLine = regexp.MustCompile(`^~{4,}$`)

// ListBlocks walks the document fence-aware: content inside a block's fence
// (including raw reviewer output) is never scanned for headers, so forged
// headers inside raw evidence cannot shadow relay-managed blocks (R1-CX-F2).
// An unterminated block is a document integrity failure and fails closed —
// it never silently truncates the scan (CP finding).
func ListBlocks(doc string) ([]Block, error) {
	lines := strings.Split(doc, "\n")
	var out []Block
	for i := 0; i < len(lines); i++ {
		m := blockHead.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		if i+1 >= len(lines) || !fenceLine.MatchString(lines[i+1]) {
			continue // header without fence: not a well-formed block
		}
		fence := lines[i+1]
		var body []string
		closed := false
		j := i + 2
		for ; j < len(lines); j++ {
			if lines[j] == fence {
				closed = true
				break
			}
			body = append(body, lines[j])
		}
		if !closed {
			return nil, fmt.Errorf("block %q unterminated at line %d: canonical integrity failure, fail-closed", m[1], i+1)
		}
		out = append(out, Block{Label: m[1], Encoding: m[2], Claimed: m[3], Body: strings.Join(body, "\n")})
		i = j
	}
	return out, nil
}

// ExtractBlock re-reads a stored block and returns the original bytes after
// verifying the recomputed digest against the recorded one. Lookup is
// fence-aware; duplicate labels outside fences fail closed as ambiguous.
func ExtractBlock(stored, label string) ([]byte, error) {
	blocks, err := ListBlocks(stored)
	if err != nil {
		return nil, err
	}
	var found []Block
	for _, b := range blocks {
		if b.Label == label {
			found = append(found, b)
		}
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("block %q not found or malformed: fail-closed", label)
	}
	if len(found) > 1 {
		return nil, fmt.Errorf("block %q is ambiguous (%d occurrences — labels must be unique per scope)", label, len(found))
	}
	b := found[0]
	var raw []byte
	if b.Encoding == "base64" {
		dec, err := base64.StdEncoding.DecodeString(b.Body)
		if err != nil {
			return nil, fmt.Errorf("block %q base64 decode: %w", label, err)
		}
		raw = dec
	} else {
		raw = []byte(b.Body)
	}
	if Digest(raw) != b.Claimed {
		return nil, fmt.Errorf("block %q digest mismatch (stored %s, recomputed %s): fail-closed",
			label, b.Claimed[:12], Digest(raw)[:12])
	}
	return raw, nil
}

// WritePrivateAtomic replaces path with owner-only bytes using a same-dir
// unique temp and atomic rename. Atomic rename is the process-crash
// correctness boundary. File and parent-directory sync are attempted as
// power-loss hardening; their failures are returned as a diagnostic but do
// not turn a completed rename into a failed write (DR-813 §D).
func WritePrivateAtomic(path string, data []byte) (diagnostic string, err error) {
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
	if _, err := f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	var diagnostics []string
	if err := f.Sync(); err != nil {
		diagnostics = append(diagnostics, "file-sync-best-effort: "+err.Error())
	}
	if err := f.Close(); err != nil {
		return strings.Join(diagnostics, "; "), err
	}
	if err := os.Rename(tmp, path); err != nil {
		return strings.Join(diagnostics, "; "), err
	}
	if diagnostic := SyncParentBestEffort(path); diagnostic != "" {
		diagnostics = append(diagnostics, diagnostic)
	}
	diagnostic = strings.Join(diagnostics, "; ")
	emitSyncDiagnostic(path, diagnostic)
	return diagnostic, nil
}

// SyncParentBestEffort attempts the power-loss hardening step for a directory
// entry. It never changes the success semantics of an already-completed
// rename/remove and returns an observable capability diagnostic instead.
func SyncParentBestEffort(path string) string {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return "parent-directory-sync-unavailable: " + err.Error()
	}
	var diagnostics []string
	if err := dir.Sync(); err != nil {
		diagnostics = append(diagnostics, "parent-directory-sync-best-effort: "+err.Error())
	}
	if err := dir.Close(); err != nil {
		diagnostics = append(diagnostics, "parent-directory-close: "+err.Error())
	}
	return strings.Join(diagnostics, "; ")
}

func emitSyncDiagnostic(path, diagnostic string) {
	if diagnostic != "" {
		// Rare capability/power-loss hardening failures remain observable while
		// the process-crash-correct atomic rename stays successful. Use only the
		// basename so diagnostics do not unnecessarily expose private paths.
		fmt.Fprintf(os.Stderr, "acrelay: sync diagnostic for %s: %s\n", filepath.Base(path), diagnostic)
	}
}

// RemovePrivate removes a private sidecar and best-effort syncs its parent
// directory. A remove failure is correctness-significant; a sync failure is
// diagnostic-only because the process-crash contract is already satisfied.
func RemovePrivate(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	emitSyncDiagnostic(path, SyncParentBestEffort(path))
	return nil

}

// RenamePrivate moves a private sidecar inside its filesystem and applies the
// same best-effort parent-directory hardening without overclaiming power-loss
// durability.
func RenamePrivate(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	emitSyncDiagnostic(to, SyncParentBestEffort(to))
	return nil
}

// AppendAtomic appends a section iff the canonical file still matches the
// pre-dispatch expectedRev. On any drift it fails closed without writing.
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
	next := append(current, []byte(section)...)
	if _, err := WritePrivateAtomic(path, next); err != nil {
		return "", err
	}
	return Digest(next), nil
}
