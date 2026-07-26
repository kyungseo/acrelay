package subject

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// symlinkOrSkip creates a symlink or skips with a capability reason: Windows
// symlink creation needs SeCreateSymbolicLinkPrivilege (admin or Developer
// Mode), absent for a standard user. The product resolves symlinks (fail-
// closed on escape) but never creates them, so a standard-user lane cannot
// stage this fixture (FEAT-20260722-002 UTM lane).
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("capability: symlink creation unavailable (%v); on Windows this needs admin or Developer Mode", err)
	}
}

func TestSingleFileUsesDomainSeparatedAggregate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "target.md")
	writeFile(t, path, "same bytes")
	spec, err := SingleFile(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	raw := sha256.Sum256([]byte("same bytes"))
	if snapshot.Aggregate == hex.EncodeToString(raw[:]) {
		t.Fatal("one-member aggregate must not collapse to the raw content digest")
	}
	if got := snapshot.Members[0].LogicalPath; got != "target.md" {
		t.Fatalf("logical path = %q", got)
	}
}

func TestScopeSummaryBroadGuardrails(t *testing.T) {
	snapshot := Snapshot{}
	for i := 0; i <= BroadScopeMemberThreshold; i++ {
		snapshot.Members = append(snapshot.Members, Member{Bytes: 1})
	}
	summary := SummarizeScope(snapshot)
	if !summary.Broad || summary.Members != BroadScopeMemberThreshold+1 ||
		!strings.Contains(strings.Join(summary.Reasons, ","), "members>") {
		t.Fatalf("member guardrail summary = %+v", summary)
	}
	byteHeavy := Snapshot{Members: []Member{{Bytes: BroadScopeByteThreshold + 1}}}
	summary = SummarizeScope(byteHeavy)
	if !summary.Broad || summary.Bytes != BroadScopeByteThreshold+1 ||
		!strings.Contains(strings.Join(summary.Reasons, ","), "bytes>") {
		t.Fatalf("byte guardrail summary = %+v", summary)
	}
}

func TestExplicitFilesNormalizeOrderAndRejectDuplicates(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	writeFile(t, filepath.Join(root, "b.txt"), "b")
	a, err := Normalize(Spec{Version: SpecVersion, Kind: KindFiles, Root: root, Members: []string{"b.txt", "a.txt"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Normalize(Spec{Version: SpecVersion, Kind: KindFiles, Root: root, Members: []string{"a.txt", "b.txt"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	sa, err := Resolve(a)
	if err != nil {
		t.Fatal(err)
	}
	sb, err := Resolve(b)
	if err != nil {
		t.Fatal(err)
	}
	if sa.Aggregate != sb.Aggregate || !reflect.DeepEqual(sa.Members, sb.Members) {
		t.Fatal("equivalent explicit sets did not canonicalize identically")
	}
	_, err = Normalize(Spec{Kind: KindFiles, Root: root, Members: []string{"a/../a.txt", "a.txt"}}, "")
	if err == nil || !strings.Contains(err.Error(), "duplicate normalized") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestLargeExplicitSetIsStableAndBytewiseSorted(t *testing.T) {
	for _, size := range []int{10, 1000} {
		t.Run(fmt.Sprintf("members-%d", size), func(t *testing.T) {
			root := t.TempDir()
			members := make([]string, 0, size)
			for i := size - 1; i >= 0; i-- {
				name := fmt.Sprintf("member-%04d.txt", i)
				writeFile(t, filepath.Join(root, name), fmt.Sprintf("value-%04d", i))
				members = append(members, name)
			}
			spec, err := Normalize(Spec{Kind: KindFiles, Root: root, Members: members}, "")
			if err != nil {
				t.Fatal(err)
			}
			one, err := Resolve(spec)
			if err != nil {
				t.Fatal(err)
			}
			two, err := Resolve(spec)
			if err != nil {
				t.Fatal(err)
			}
			if len(one.Members) != size || one.Aggregate != two.Aggregate {
				t.Fatalf("set was not stable: members=%d", len(one.Members))
			}
			for i := 1; i < len(one.Members); i++ {
				if one.Members[i-1].LogicalPath >= one.Members[i].LogicalPath {
					t.Fatalf("members not bytewise sorted at %d", i)
				}
			}
		})
	}
}

func TestSubtreeMembershipAndContentChangesAffectAggregate(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "aa")
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref")
	spec, err := Normalize(Spec{Kind: KindSubtree, Root: root}, "")
	if err != nil {
		t.Fatal(err)
	}
	initial, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Members) != 2 {
		t.Fatalf("default subtree members = %d, want 2", len(initial.Members))
	}

	writeFile(t, filepath.Join(root, "a.txt"), "bb") // same length
	modified, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if modified.Aggregate == initial.Aggregate {
		t.Fatal("same-size content change was not detected")
	}

	writeFile(t, filepath.Join(root, "new.txt"), "new")
	created, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if created.Aggregate == modified.Aggregate {
		t.Fatal("member creation was not detected")
	}
	if err := os.Rename(filepath.Join(root, "new.txt"), filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	renamed, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Aggregate == created.Aggregate {
		t.Fatal("member rename was not detected")
	}
	if err := os.Remove(filepath.Join(root, "renamed.txt")); err != nil {
		t.Fatal(err)
	}
	deleted, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Aggregate == renamed.Aggregate {
		t.Fatal("member deletion was not detected")
	}

	excluded, err := Normalize(Spec{Kind: KindSubtree, Root: root, Exclude: []string{".git"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	excludedSnapshot, err := Resolve(excluded)
	if err != nil {
		t.Fatal(err)
	}
	if len(excludedSnapshot.Members) != 1 || excludedSnapshot.Members[0].LogicalPath != "a.txt" {
		t.Fatalf("explicit exclusion members = %#v", excludedSnapshot.Members)
	}
}

func TestSymlinkIdentityAndRootContainmentAreFailClosed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "one.txt"), "same")
	writeFile(t, filepath.Join(root, "two.txt"), "same")
	link := filepath.Join(root, "current.txt")
	symlinkOrSkip(t, "one.txt", link)
	spec, err := SingleFile(link)
	if err != nil {
		t.Fatal(err)
	}
	one, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("two.txt", link); err != nil {
		t.Fatal(err)
	}
	two, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if one.Aggregate == two.Aggregate {
		t.Fatal("same-byte symlink retarget was not detected")
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeFile(t, outside, "outside")
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(spec); err == nil || !strings.Contains(err.Error(), "escapes declared root") {
		t.Fatalf("root escape error = %v", err)
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing.txt", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(spec); err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("broken symlink error = %v", err)
	}
}

func TestSymlinkChainIdentityIsRecorded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "target.txt"), "target")
	symlinkOrSkip(t, "target.txt", filepath.Join(root, "leaf.txt"))
	symlinkOrSkip(t, "leaf.txt", filepath.Join(root, "chain.txt"))
	spec, err := SingleFile(filepath.Join(root, "chain.txt"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	member := snapshot.Members[0]
	if member.Kind != "symlink" || member.LinkTarget != "leaf.txt" || member.ResolvedPath != "target.txt" {
		t.Fatalf("chain identity = %+v", member)
	}
}

func TestInvalidSelectorsAndPersistedTamperingFailClosed(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "a")
	// TR-CX-F1 regression table: rejection must be host-invariant — the same
	// raw identifier fails closed identically on macOS, Linux, and Windows.
	for _, bad := range []string{
		"../a.txt", "/a.txt", "bad\x00path", string([]byte{0xff}),
		"\\a.txt", "a\\b.txt", "a\\..\\secret", "C:secret", "C:/secret",
	} {
		_, err := Normalize(Spec{Kind: KindFiles, Root: root, Members: []string{bad}}, "")
		if err == nil {
			t.Fatalf("invalid logical path %q accepted", bad)
		}
	}
	if _, err := Resolve(Spec{Kind: KindSubtree, Root: filepath.Join(root, "empty")}); err == nil {
		t.Fatal("unreadable/empty subtree accepted")
	}
	if _, err := Resolve(Spec{Kind: KindFiles, Root: root, Members: []string{"missing.txt"}}); err == nil {
		t.Fatal("missing explicit member accepted")
	}

	spec, err := SingleFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	tampered := snapshot
	tampered.Members = append([]Member(nil), snapshot.Members...)
	tampered.Members[0].Bytes++
	if err := ValidatePersisted(spec, tampered); err == nil {
		t.Fatal("tampered member metadata accepted")
	}
	tampered = snapshot
	tampered.Aggregate = strings.Repeat("0", 64)
	if err := ValidatePersisted(spec, tampered); err == nil {
		t.Fatal("tampered aggregate accepted")
	}
}

func TestAggregateSeparatesTypedAndLengthPrefixedFields(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), "x")
	spec, err := SingleFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	base := snapshot.Aggregate

	typed := snapshot
	typed.Members = append([]Member(nil), snapshot.Members...)
	typed.Members[0].Kind = "resolved-file"
	typedAggregate, err := Aggregate(spec, typed)
	if err != nil {
		t.Fatal(err)
	}
	if typedAggregate == base {
		t.Fatal("typed member fields were not domain separated")
	}

	length := snapshot
	length.Members = append([]Member(nil), snapshot.Members...)
	length.Members[0].LinkTarget = "x\x00y"
	if _, err := Aggregate(spec, length); err == nil {
		t.Fatal("invalid variable-length field accepted")
	}
}

func TestLoadSpecUsesDescriptorDirectoryAndRejectsUnknownFields(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "subject", "a.txt"), "a")
	specPath := filepath.Join(root, "spec.json")
	writeFile(t, specPath, `{"version":"subject-spec v0.1","kind":"files","root":"subject","members":["a.txt"]}`)
	spec, err := LoadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Root != filepath.Join(root, "subject") {
		t.Fatalf("root = %q", spec.Root)
	}
	writeFile(t, specPath, `{"kind":"subtree","root":"subject","surprise":true}`)
	if _, err := LoadSpec(specPath); err == nil {
		t.Fatal("unknown field accepted")
	}
	if err := os.WriteFile(specPath, []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(specPath); err == nil {
		t.Fatal("invalid UTF-8 descriptor accepted")
	}
}

func TestSelectorMembershipAndDynamicFilenamePrefix(t *testing.T) {
	root := t.TempDir()

	files, err := Normalize(Spec{
		Kind: KindFiles, Root: root,
		Members: []string{"src/a.go", "state/review.md.dispatch-fixed.json"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectsLogical(files, "src/a.go")
	if err != nil || !selected {
		t.Fatalf("explicit member selection = %v, %v", selected, err)
	}
	selected, err = SelectsLogical(files, "src/b.go")
	if err != nil || selected {
		t.Fatalf("undeclared explicit member selection = %v, %v", selected, err)
	}
	selected, err = MaySelectNewFilenamePrefix(files, "state/review.md.dispatch-")
	if err != nil || selected {
		t.Fatalf("explicit membership must not grow with a dynamic namespace: %v, %v", selected, err)
	}

	subtree, err := Normalize(Spec{Kind: KindSubtree, Root: root, Include: []string{"src"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	selected, err = SelectsLogical(subtree, "src/a.go")
	if err != nil || !selected {
		t.Fatalf("included subtree member = %v, %v", selected, err)
	}
	selected, err = MaySelectNewFilenamePrefix(subtree, "review.md.dispatch-")
	if err != nil || selected {
		t.Fatalf("unselected root runtime namespace = %v, %v", selected, err)
	}

	excluded, err := Normalize(Spec{Kind: KindSubtree, Root: root, Exclude: []string{".acrelay"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	selected, err = MaySelectNewFilenamePrefix(excluded, ".acrelay/review.md.dispatch-")
	if err != nil || selected {
		t.Fatalf("directory-excluded runtime namespace = %v, %v", selected, err)
	}

	filenameOnly, err := Normalize(Spec{
		Kind: KindSubtree, Root: root,
		Exclude: []string{"review.md", "review.md.lock"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	selected, err = MaySelectNewFilenamePrefix(filenameOnly, "review.md.dispatch-")
	if err != nil || !selected {
		t.Fatalf("filename-only exclusions must not cover a dynamic namespace: %v, %v", selected, err)
	}
}
