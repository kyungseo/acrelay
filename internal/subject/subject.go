// Package subject resolves local review subjects into deterministic manifests.
// It deliberately owns no Git or network integration: selectors are bounded to
// one local file, an explicit local file set, or a declared local subtree.
package subject

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	SpecVersion = "subject-spec v0.1"
	domainTag   = "acrelay-subject-v1\x00"
	// BroadScopeMemberThreshold and BroadScopeByteThreshold are conservative
	// first-use guardrails calibrated against the 2026-07-26 dogfood run:
	// 9 members / ~177 KiB produced a 16-turn, 10m26s reviewer invocation.
	BroadScopeMemberThreshold = 8
	BroadScopeByteThreshold   = 128 * 1024
)

type Kind string

const (
	KindFile    Kind = "file"
	KindFiles   Kind = "files"
	KindSubtree Kind = "subtree"
)

// Spec is the immutable selector persisted in the canonical state. Root is
// absolute after Normalize. Members, Include, and Exclude are normalized,
// root-relative slash paths. Include/exclude use path-prefix semantics; an
// empty Include selects every file and an empty Exclude excludes nothing.
type Spec struct {
	Version string   `json:"version"`
	Kind    Kind     `json:"kind"`
	Root    string   `json:"root"`
	Members []string `json:"members,omitempty"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// Member is one resolved local byte source. ResolvedPath is relative to the
// resolved root. LinkTarget is the leaf symlink payload when the logical path
// itself is a symlink; parent/root symlink identity is captured by the resolved
// root/path pair.
type Member struct {
	Kind         string `json:"kind"`
	LogicalPath  string `json:"logical_path"`
	ResolvedPath string `json:"resolved_path"`
	LinkTarget   string `json:"link_target,omitempty"`
	Digest       string `json:"digest"`
	Bytes        int64  `json:"bytes"`
}

// Snapshot is the resolved manifest stored in canonical history. Aggregate is
// a domain-separated digest over the selector and every typed member record.
type Snapshot struct {
	ResolvedRoot string   `json:"resolved_root"`
	Members      []Member `json:"members"`
	Aggregate    string   `json:"aggregate"`
}

// ScopeSummary is a bounded pre-dispatch view that contains no member paths or
// content. Broad is a consent gate, not a claim that a smaller subject is fast.
type ScopeSummary struct {
	Members int
	Bytes   int64
	Broad   bool
	Reasons []string
}

func SummarizeScope(snapshot Snapshot) ScopeSummary {
	return SummarizeScopes(snapshot)
}

// SummarizeScopes combines authoritative and auxiliary manifests for one
// user-visible dispatch appetite check.
func SummarizeScopes(snapshots ...Snapshot) ScopeSummary {
	summary := ScopeSummary{}
	for _, snapshot := range snapshots {
		summary.Members += len(snapshot.Members)
		for _, member := range snapshot.Members {
			summary.Bytes += member.Bytes
		}
	}
	if summary.Members > BroadScopeMemberThreshold {
		summary.Broad = true
		summary.Reasons = append(summary.Reasons,
			fmt.Sprintf("members>%d", BroadScopeMemberThreshold))
	}
	if summary.Bytes > BroadScopeByteThreshold {
		summary.Broad = true
		summary.Reasons = append(summary.Reasons,
			fmt.Sprintf("bytes>%d", BroadScopeByteThreshold))
	}
	return summary
}

// SingleFile constructs the legacy -target shorthand as a one-member spec.
func SingleFile(location string) (Spec, error) {
	abs, err := filepath.Abs(location)
	if err != nil {
		return Spec{}, fmt.Errorf("target absolute path: %w", err)
	}
	return Normalize(Spec{
		Version: SpecVersion,
		Kind:    KindFile,
		Root:    filepath.Dir(abs),
		Members: []string{filepath.Base(abs)},
	}, "")
}

// LoadSpec reads a selector descriptor. A relative root is resolved against
// the descriptor directory; the descriptor itself is not a target member.
func LoadSpec(path string) (Spec, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Spec{}, err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Spec{}, fmt.Errorf("read subject spec: %w", err)
	}
	if !utf8.Valid(b) {
		return Spec{}, fmt.Errorf("parse subject spec: descriptor must be valid UTF-8: fail-closed")
	}
	var spec Spec
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("parse subject spec: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Spec{}, fmt.Errorf("parse subject spec: trailing JSON value: fail-closed")
	}
	return Normalize(spec, filepath.Dir(abs))
}

// Normalize validates and canonicalizes a selector without reading members.
func Normalize(spec Spec, baseDir string) (Spec, error) {
	if spec.Version == "" {
		spec.Version = SpecVersion
	}
	if spec.Version != SpecVersion {
		return Spec{}, fmt.Errorf("subject spec version %q unsupported (want %q): fail-closed", spec.Version, SpecVersion)
	}
	switch spec.Kind {
	case KindFile, KindFiles, KindSubtree:
	default:
		return Spec{}, fmt.Errorf("subject selector kind %q unsupported: fail-closed", spec.Kind)
	}
	if strings.TrimSpace(spec.Root) == "" {
		return Spec{}, fmt.Errorf("subject root is required: fail-closed")
	}
	if !utf8.ValidString(spec.Root) || strings.IndexByte(spec.Root, 0) >= 0 {
		return Spec{}, fmt.Errorf("subject root must be valid UTF-8 without NUL: fail-closed")
	}
	root := spec.Root
	if !filepath.IsAbs(root) {
		if baseDir == "" {
			var err error
			baseDir, err = os.Getwd()
			if err != nil {
				return Spec{}, err
			}
		}
		root = filepath.Join(baseDir, root)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Spec{}, fmt.Errorf("subject root absolute path: %w", err)
	}
	spec.Root = filepath.Clean(absRoot)

	spec.Members, err = normalizeList(spec.Members, "member")
	if err != nil {
		return Spec{}, err
	}
	spec.Include, err = normalizeList(spec.Include, "include")
	if err != nil {
		return Spec{}, err
	}
	spec.Exclude, err = normalizeList(spec.Exclude, "exclude")
	if err != nil {
		return Spec{}, err
	}

	switch spec.Kind {
	case KindFile:
		if len(spec.Members) != 1 || len(spec.Include) != 0 || len(spec.Exclude) != 0 {
			return Spec{}, fmt.Errorf("file selector requires exactly one member and no include/exclude: fail-closed")
		}
	case KindFiles:
		if len(spec.Members) == 0 || len(spec.Include) != 0 || len(spec.Exclude) != 0 {
			return Spec{}, fmt.Errorf("files selector requires members and no include/exclude: fail-closed")
		}
	case KindSubtree:
		if len(spec.Members) != 0 {
			return Spec{}, fmt.Errorf("subtree selector cannot carry explicit members: fail-closed")
		}
	}
	return spec, nil
}

func normalizeList(values []string, field string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		logical, err := normalizeLogical(raw)
		if err != nil {
			return nil, fmt.Errorf("subject %s %q: %w", field, raw, err)
		}
		if seen[logical] {
			return nil, fmt.Errorf("duplicate normalized subject %s %q: fail-closed", field, logical)
		}
		seen[logical] = true
		out = append(out, logical)
	}
	sort.Strings(out)
	return out, nil
}

// normalizeLogical validates and normalizes a logical member identifier
// under an OS-independent grammar (TR-CX-F1): logical paths are slash-only,
// root-relative identifiers whose admission and normalized identity are
// byte-identical on every host. Backslashes are rejected at any position
// (separator on Windows, literal elsewhere — either way host-divergent), a
// Windows drive prefix is rejected on every host, and normalization uses
// slash-only path.Clean. filepath conversion happens only at the filesystem
// resolution stage, never here.
func normalizeLogical(raw string) (string, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.IndexByte(raw, 0) >= 0 {
		return "", fmt.Errorf("logical path must be non-empty valid UTF-8 without NUL: fail-closed")
	}
	if strings.ContainsRune(raw, '\\') {
		return "", fmt.Errorf("logical path must not contain a backslash: fail-closed")
	}
	if len(raw) >= 2 && raw[1] == ':' &&
		(('a' <= raw[0] && raw[0] <= 'z') || ('A' <= raw[0] && raw[0] <= 'Z')) {
		return "", fmt.Errorf("logical path must not carry a drive prefix: fail-closed")
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("logical path must be root-relative: fail-closed")
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("logical path escapes or aliases the root: fail-closed")
	}
	return clean, nil
}

func resolveRoot(spec Spec) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(spec.Root)
	if err != nil {
		return "", fmt.Errorf("resolve subject root %s: %w: fail-closed", spec.Root, err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return "", err
	}
	if !utf8.ValidString(resolvedRoot) || strings.IndexByte(resolvedRoot, 0) >= 0 {
		return "", fmt.Errorf("resolved subject root must be valid UTF-8 without NUL: fail-closed")
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil {
		return "", fmt.Errorf("subject root %s unreadable: %w", spec.Root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("subject root %s is not a directory: fail-closed", spec.Root)
	}
	return resolvedRoot, nil
}

// ResolveRoot returns the selector's current resolved root without reading
// any members. Callers use it for non-consuming path-boundary preflight; a
// later authoritative Resolve is still required before state is persisted.
func ResolveRoot(input Spec) (string, error) {
	spec, err := Normalize(input, "")
	if err != nil {
		return "", err
	}
	return resolveRoot(spec)
}

// SelectsLogical reports whether a normalized logical file path belongs to
// the selector. It does not touch disk.
func SelectsLogical(input Spec, raw string) (bool, error) {
	spec, err := Normalize(input, "")
	if err != nil {
		return false, err
	}
	logical, err := normalizeLogical(raw)
	if err != nil {
		return false, err
	}
	switch spec.Kind {
	case KindFile, KindFiles:
		i := sort.SearchStrings(spec.Members, logical)
		return i < len(spec.Members) && spec.Members[i] == logical, nil
	case KindSubtree:
		included := len(spec.Include) == 0 || matchesPrefix(logical, spec.Include)
		return included && !matchesPrefix(logical, spec.Exclude), nil
	default:
		return false, fmt.Errorf("subject selector kind %q unsupported: fail-closed", spec.Kind)
	}
}

// MaySelectNewFilenamePrefix reports whether a newly created file whose
// logical name starts with prefix can enter the selector. Explicit file sets
// have immutable membership and therefore return false. A subtree is safe only
// when its containing directory is excluded; excluding one possible filename
// cannot cover a dynamic namespace.
func MaySelectNewFilenamePrefix(input Spec, rawPrefix string) (bool, error) {
	spec, err := Normalize(input, "")
	if err != nil {
		return false, err
	}
	prefix, err := normalizeLogical(rawPrefix)
	if err != nil {
		return false, err
	}
	switch spec.Kind {
	case KindFile, KindFiles:
		return false, nil
	case KindSubtree:
		parent := path.Dir(prefix)
		if parent != "." && matchesPrefix(parent, spec.Exclude) {
			return false, nil
		}
		if len(spec.Include) == 0 {
			return true, nil
		}
		probe := prefix + "candidate"
		if matchesPrefix(probe, spec.Include) {
			return true, nil
		}
		for _, include := range spec.Include {
			if include != prefix && strings.HasPrefix(include, prefix) {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, fmt.Errorf("subject selector kind %q unsupported: fail-closed", spec.Kind)
	}
}

// Resolve reads the selector's current members and computes its aggregate.
func Resolve(input Spec) (Snapshot, error) {
	spec, err := Normalize(input, "")
	if err != nil {
		return Snapshot{}, err
	}
	resolvedRoot, err := resolveRoot(spec)
	if err != nil {
		return Snapshot{}, err
	}

	logicalPaths := append([]string(nil), spec.Members...)
	if spec.Kind == KindSubtree {
		logicalPaths, err = enumerateSubtree(resolvedRoot, spec.Include, spec.Exclude)
		if err != nil {
			return Snapshot{}, err
		}
	}
	if len(logicalPaths) == 0 {
		return Snapshot{}, fmt.Errorf("subject selector resolved zero members: fail-closed")
	}

	members := make([]Member, 0, len(logicalPaths))
	for _, logical := range logicalPaths {
		member, err := resolveMember(resolvedRoot, logical)
		if err != nil {
			return Snapshot{}, err
		}
		members = append(members, member)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].LogicalPath < members[j].LogicalPath })
	for i := 1; i < len(members); i++ {
		if members[i-1].LogicalPath == members[i].LogicalPath {
			return Snapshot{}, fmt.Errorf("duplicate resolved logical path %q: fail-closed", members[i].LogicalPath)
		}
	}

	snapshot := Snapshot{ResolvedRoot: resolvedRoot, Members: members}
	aggregate, err := Aggregate(spec, snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Aggregate = aggregate
	return snapshot, nil
}

func enumerateSubtree(root string, include, exclude []string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk subject subtree: %w", walkErr)
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		logical, err := normalizeLogical(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		if matchesPrefix(logical, exclude) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if len(include) > 0 && !matchesPrefix(logical, include) {
			return nil
		}
		paths = append(paths, logical)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func matchesPrefix(logical string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if logical == prefix || strings.HasPrefix(logical, prefix+"/") {
			return true
		}
	}
	return false
}

func resolveMember(root, logical string) (Member, error) {
	original := filepath.Join(root, filepath.FromSlash(logical))
	linfo, err := os.Lstat(original)
	if err != nil {
		return Member{}, fmt.Errorf("subject member %s unreadable: %w", logical, err)
	}
	resolved, err := filepath.EvalSymlinks(original)
	if err != nil {
		return Member{}, fmt.Errorf("resolve subject member %s: %w: fail-closed", logical, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return Member{}, err
	}
	resolvedLogical, err := relativeWithin(root, resolved)
	if err != nil {
		return Member{}, fmt.Errorf("subject member %s: %w", logical, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Member{}, fmt.Errorf("stat subject member %s: %w", logical, err)
	}
	if !info.Mode().IsRegular() {
		return Member{}, fmt.Errorf("subject member %s does not resolve to a regular file: fail-closed", logical)
	}
	b, err := os.ReadFile(resolved)
	if err != nil {
		return Member{}, fmt.Errorf("read subject member %s: %w", logical, err)
	}
	digest := sha256.Sum256(b)
	kind := "file"
	linkTarget := ""
	if linfo.Mode()&os.ModeSymlink != 0 {
		kind = "symlink"
		linkTarget, err = os.Readlink(original)
		if err != nil {
			return Member{}, fmt.Errorf("read subject symlink %s: %w", logical, err)
		}
	} else if filepath.Clean(original) != filepath.Clean(resolved) {
		kind = "resolved-file"
	}
	return Member{
		Kind: kind, LogicalPath: logical, ResolvedPath: resolvedLogical,
		LinkTarget: linkTarget, Digest: hex.EncodeToString(digest[:]), Bytes: int64(len(b)),
	}, nil
}

func relativeWithin(root, resolved string) (string, error) {
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", err
	}
	if rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved path escapes declared root: fail-closed")
	}
	logical, err := normalizeLogical(filepath.ToSlash(rel))
	if err != nil {
		return "", err
	}
	return logical, nil
}

// Aggregate recomputes the deterministic aggregate from persisted fields. It
// does not read disk, so LoadState can reject tampered manifest metadata while
// still allowing a legitimately stale on-disk subject to load.
func Aggregate(input Spec, snapshot Snapshot) (string, error) {
	spec, err := Normalize(input, "")
	if err != nil {
		return "", err
	}
	if snapshot.ResolvedRoot == "" || !filepath.IsAbs(snapshot.ResolvedRoot) ||
		!utf8.ValidString(snapshot.ResolvedRoot) || strings.IndexByte(snapshot.ResolvedRoot, 0) >= 0 {
		return "", fmt.Errorf("subject resolved root must be absolute: fail-closed")
	}
	if len(snapshot.Members) == 0 {
		return "", fmt.Errorf("subject snapshot has no members: fail-closed")
	}

	members := append([]Member(nil), snapshot.Members...)
	sort.Slice(members, func(i, j int) bool { return members[i].LogicalPath < members[j].LogicalPath })
	var encoded []byte
	encoded = append(encoded, domainTag...)
	encoded = appendString(encoded, spec.Version)
	encoded = appendString(encoded, string(spec.Kind))
	encoded = appendString(encoded, spec.Root)
	encoded = appendStrings(encoded, spec.Members)
	encoded = appendStrings(encoded, spec.Include)
	encoded = appendStrings(encoded, spec.Exclude)
	encoded = appendString(encoded, filepath.Clean(snapshot.ResolvedRoot))
	encoded = binary.AppendUvarint(encoded, uint64(len(members)))
	last := ""
	for i, member := range members {
		logical, err := normalizeLogical(member.LogicalPath)
		if err != nil || logical != member.LogicalPath {
			return "", fmt.Errorf("persisted subject logical path %q invalid: fail-closed", member.LogicalPath)
		}
		if i > 0 && logical == last {
			return "", fmt.Errorf("duplicate persisted logical path %q: fail-closed", logical)
		}
		last = logical
		resolvedLogical, err := normalizeLogical(member.ResolvedPath)
		if err != nil || resolvedLogical != member.ResolvedPath {
			return "", fmt.Errorf("persisted resolved path %q invalid: fail-closed", member.ResolvedPath)
		}
		if member.Kind != "file" && member.Kind != "symlink" && member.Kind != "resolved-file" {
			return "", fmt.Errorf("persisted subject member kind %q invalid: fail-closed", member.Kind)
		}
		if member.Bytes < 0 || !utf8.ValidString(member.LinkTarget) || strings.IndexByte(member.LinkTarget, 0) >= 0 {
			return "", fmt.Errorf("persisted subject member metadata invalid: fail-closed")
		}
		if (member.Kind == "symlink") != (member.LinkTarget != "") {
			return "", fmt.Errorf("persisted subject symlink metadata is inconsistent: fail-closed")
		}
		digest, err := hex.DecodeString(member.Digest)
		if err != nil || len(digest) != sha256.Size || member.Digest != strings.ToLower(member.Digest) {
			return "", fmt.Errorf("persisted subject member digest %q invalid: fail-closed", member.Digest)
		}
		encoded = appendString(encoded, member.Kind)
		encoded = appendString(encoded, logical)
		encoded = appendString(encoded, resolvedLogical)
		encoded = appendString(encoded, member.LinkTarget)
		encoded = binary.AppendUvarint(encoded, uint64(member.Bytes))
		encoded = append(encoded, digest...)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ValidatePersisted verifies that stored manifest fields reproduce the stored
// aggregate without consulting disk.
func ValidatePersisted(spec Spec, snapshot Snapshot) error {
	normalized, err := Normalize(spec, "")
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(spec, normalized) {
		return fmt.Errorf("persisted subject spec is not canonical: fail-closed")
	}
	if filepath.Clean(snapshot.ResolvedRoot) != snapshot.ResolvedRoot {
		return fmt.Errorf("persisted subject resolved root is not canonical: fail-closed")
	}
	for i := 1; i < len(snapshot.Members); i++ {
		if snapshot.Members[i-1].LogicalPath >= snapshot.Members[i].LogicalPath {
			return fmt.Errorf("persisted subject members are not strictly sorted: fail-closed")
		}
	}
	aggregate, err := Aggregate(spec, snapshot)
	if err != nil {
		return err
	}
	if snapshot.Aggregate != aggregate {
		return fmt.Errorf("subject aggregate %q does not match persisted manifest %q: fail-closed", snapshot.Aggregate, aggregate)
	}
	return nil
}

func appendString(dst []byte, value string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func appendStrings(dst []byte, values []string) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(values)))
	for _, value := range values {
		dst = appendString(dst, value)
	}
	return dst
}

// Summary returns a stable human-readable selector description.
func Summary(spec Spec, snapshot Snapshot) string {
	scope := SummarizeScope(snapshot)
	return fmt.Sprintf("%s root=%s members=%d bytes=%d broad=%v",
		spec.Kind, spec.Root, scope.Members, scope.Bytes, scope.Broad)
}
