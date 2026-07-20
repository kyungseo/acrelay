package relay

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/subject"
)

// This marker is deliberately confined to test code. The fixture exercises
// the synthetic-sampled contract surface; it is not evidence that a live LLM
// understood or would reliably detect defects in a user target.
const syntheticDefectSeedMarker = "ACRELAY_SYNTHETIC_DEFECT_SEED"

func TestSyntheticSeedFixtureUsesFreshSessionsOnly(t *testing.T) {
	seenSessions := map[string]bool{}
	for sample := 0; sample < 3; sample++ {
		dir := t.TempDir()
		target := filepath.Join(dir, "synthetic.go")
		line := fmt.Sprintf("package synthetic // %s sample-%d", syntheticDefectSeedMarker, sample)
		if err := os.WriteFile(target, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		result := adapter.FakeResult{Structured: map[string]any{
			"verdict": "changes-requested",
			"examined": []any{map[string]any{
				"id": "E1", "member": "synthetic.go",
				"location": map[string]any{"kind": "text-lines", "start": 1, "end": 1},
				"excerpt":  line, "claim": "examined synthetic seed fixture",
			}},
			"findings": []any{map[string]any{
				"summary": "synthetic seeded defect observed", "reviewer_severity": "high",
				"evidence": []any{"E1"}, "recommendation": "remove the synthetic seed",
			}},
			"approval_requests": []any{},
		}}
		fake := &adapter.FakeAdapter{
			VendorName: "fake", NativeHandle: fmt.Sprintf("fresh-synthetic-%d", sample),
			Script: []adapter.FakeResult{result},
		}
		s := &Session{
			Adapter: fake, Handles: &adapter.HandleStore{Path: filepath.Join(dir, "handles.json")},
			Canonical: filepath.Join(dir, "canonical.md"),
		}
		spec, err := subject.SingleFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := InitSubject(s.Canonical, "synthetic contract sample", spec, "", "", false, approvedPolicy(t)); err != nil {
			t.Fatal(err)
		}
		st, outcome, err := s.Review(context.Background(), "inspect the synthetic fixture", adapter.Request{})
		if err != nil || outcome != review.OutcomeResultValid {
			t.Fatalf("sample %d: outcome=%s err=%v", sample, outcome, err)
		}
		if seenSessions[st.SessionRef] || st.SessionRef == "" {
			t.Fatalf("sample %d reused or omitted a reviewer session: %q", sample, st.SessionRef)
		}
		seenSessions[st.SessionRef] = true
		if len(st.Evidence) != 1 || st.Evidence[0].Assurance != review.AssuranceContentMatch {
			t.Fatalf("sample %d lacks content-match evidence: %+v", sample, st.Evidence)
		}
	}
}

func TestSyntheticSeedMechanismsAbsentFromProductionAndScripts(t *testing.T) {
	root := filepath.Clean("../..")
	scriptSeedPattern := regexp.MustCompile(`(?i)(synthetic.{0,20}seed|seed.{0,20}defect)`)
	identifierFragments := []string{"syntheticseed", "defectseed", "seeddefect"}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(filepath.ToSlash(rel), "scripts/") {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(b), syntheticDefectSeedMarker) || scriptSeedPattern.Match(b) {
				return fmt.Errorf("synthetic seed mechanism leaked into script: %s", path)
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), syntheticDefectSeedMarker) {
			return fmt.Errorf("synthetic seed marker leaked into production source: %s", path)
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, b, 0)
		if err != nil {
			return err
		}
		var forbidden string
		ast.Inspect(file, func(node ast.Node) bool {
			ident, ok := node.(*ast.Ident)
			if !ok {
				return true
			}
			name := strings.ToLower(ident.Name)
			for _, fragment := range identifierFragments {
				if strings.Contains(name, fragment) {
					forbidden = ident.Name
					return false
				}
			}
			return true
		})
		if forbidden != "" {
			return fmt.Errorf("synthetic seed identifier %q leaked into production source: %s", forbidden, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
