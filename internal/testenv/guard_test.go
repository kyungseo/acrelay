package testenv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/kyungseo/acrelay"

type packageFacts struct {
	importPath     string
	hasTests       bool
	hasIsolation   bool
	directRisk     bool
	imports        map[string]bool
	networkImports []string
	vendorEscapes  []string
}

func TestRiskyPackagesUseIsolatedTestMain(t *testing.T) {
	root := moduleRoot(t)
	facts, err := scanModule(root)
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, importPath := range requiredIsolatedPackages(facts) {
		if !facts[importPath].hasIsolation {
			missing = append(missing, importPath)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("risky test packages must call testenv.RunIsolatedMain in TestMain:\n%s",
			strings.Join(missing, "\n"))
	}
}

func TestProductionOwnsNoNetworkClient(t *testing.T) {
	facts, err := scanModule(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	for path, fact := range facts {
		for _, imported := range fact.networkImports {
			violations = append(violations, fmt.Sprintf("%s imports %s", path, imported))
		}
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("production network ownership requires explicit policy before implementation:\n%s",
			strings.Join(violations, "\n"))
	}
}

func TestVendorCommandsRemainBareNames(t *testing.T) {
	facts, err := scanModule(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var violations []string
	for _, fact := range facts {
		violations = append(violations, fact.vendorEscapes...)
	}
	sort.Strings(violations)
	if len(violations) != 0 {
		t.Fatalf("vendor command bypasses the PATH sentinel:\n%s", strings.Join(violations, "\n"))
	}
}

func TestRequiredPackagesPropagateTransitively(t *testing.T) {
	facts := map[string]*packageFacts{
		modulePath + "/internal/adapter": {importPath: modulePath + "/internal/adapter", directRisk: true},
		modulePath + "/internal/bridge": {
			importPath: modulePath + "/internal/bridge",
			imports:    map[string]bool{modulePath + "/internal/adapter": true},
		},
		modulePath + "/internal/consumer": {
			importPath: modulePath + "/internal/consumer", hasTests: true,
			imports: map[string]bool{modulePath + "/internal/bridge": true},
		},
		modulePath + "/internal/pure": {importPath: modulePath + "/internal/pure", hasTests: true},
	}
	got := requiredIsolatedPackages(facts)
	want := []string{modulePath + "/internal/consumer"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("required packages=%v want %v", got, want)
	}
}

func TestNetworkTripwireIgnoresTestOnlyImports(t *testing.T) {
	root := t.TempDir()
	writeGoFixture(t, root, "client.go", "package fixture\nimport _ \"net/http\"\n")
	writeGoFixture(t, root, "client_test.go", "package fixture\nimport _ \"net\"\n")
	facts, err := scanModuleAs(root, modulePath)
	if err != nil {
		t.Fatal(err)
	}
	fact := facts[modulePath]
	if len(fact.networkImports) != 1 || fact.networkImports[0] != "net/http" {
		t.Fatalf("production network imports=%v, want [net/http]", fact.networkImports)
	}
}

func TestVendorCommandGuardRejectsSentinelBypass(t *testing.T) {
	root := t.TempDir()
	adapterDir := filepath.Join(root, "internal", "adapter")
	if err := os.MkdirAll(adapterDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeGoFixture(t, adapterDir, "adapter.go", `package adapter
import "os/exec"
func bad() { _ = exec.Command("/usr/local/bin/claude") }
func alsoBad() { _ = exec.Command("sh", "-c", "codex exec") }
func good() { _ = exec.Command("claude") }
`)
	facts, err := scanModuleAs(root, modulePath)
	if err != nil {
		t.Fatal(err)
	}
	escapes := facts[modulePath+"/internal/adapter"].vendorEscapes
	if len(escapes) != 2 {
		t.Fatalf("vendor escape issues=%v, want two absolute/shell bypasses", escapes)
	}
}

func scanModule(root string) (map[string]*packageFacts, error) {
	return scanModuleAs(root, modulePath)
}

func scanModuleAs(root, module string) (map[string]*packageFacts, error) {
	facts := make(map[string]*packageFacts)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		relDir, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		importPath := module
		if relDir != "." {
			importPath += "/" + filepath.ToSlash(relDir)
		}
		fact := facts[importPath]
		if fact == nil {
			fact = &packageFacts{importPath: importPath, imports: make(map[string]bool)}
			facts[importPath] = fact
		}
		isTest := strings.HasSuffix(path, "_test.go")
		fact.hasTests = fact.hasTests || isTest
		return scanGoFile(path, importPath, module, isTest, fact)
	})
	return facts, err
}

func scanGoFile(path, importPath, module string, isTest bool, fact *packageFacts) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return err
	}
	aliases := make(map[string]string)
	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			return err
		}
		alias := filepath.Base(imported)
		if spec.Name != nil {
			alias = spec.Name.Name
		}
		aliases[alias] = imported
		if strings.HasPrefix(imported, module+"/") {
			fact.imports[imported] = true
		}
		if imported == "os/exec" {
			fact.directRisk = true
		}
		if !isTest && (imported == "net" || imported == "net/http") {
			fact.networkImports = append(fact.networkImports, imported)
		}
	}

	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
			ident, ok := selector.X.(*ast.Ident)
			if ok && aliases[ident.Name] == "os/exec" &&
				(selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext") {
				fact.directRisk = true
			}
			if ok && aliases[ident.Name] == "os" && selector.Sel.Name == "UserHomeDir" {
				fact.directRisk = true
			}
		}
		if isTest && callsRunIsolatedMain(call, aliases) {
			fact.hasIsolation = true
		}
		if !isTest && importPath == module+"/internal/adapter" {
			fact.vendorEscapes = append(fact.vendorEscapes, vendorEscapeIssues(path, call, aliases)...)
		}
		return true
	})
	if importPath == module+"/internal/testenv" {
		fact.directRisk = false // the isolator itself is the trusted boundary
	}
	return nil
}

func callsRunIsolatedMain(call *ast.CallExpr, aliases map[string]string) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "RunIsolatedMain" {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	return ok && aliases[ident.Name] == modulePath+"/internal/testenv"
}

func vendorEscapeIssues(path string, call *ast.CallExpr, aliases map[string]string) []string {
	checkLiterals := false
	if ident, ok := call.Fun.(*ast.Ident); ok {
		checkLiterals = ident.Name == "probeVersion" || ident.Name == "runBoundedProbe" || ident.Name == "newGroupCmd"
	}
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
		if ident, ok := selector.X.(*ast.Ident); ok && aliases[ident.Name] == "os/exec" &&
			(selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext") {
			checkLiterals = true
		}
	}
	if !checkLiterals {
		return nil
	}
	var issues []string
	for _, arg := range call.Args {
		literal, ok := arg.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			continue
		}
		value, err := strconv.Unquote(literal.Value)
		if err != nil || (!strings.Contains(value, "claude") && !strings.Contains(value, "codex")) {
			continue
		}
		if value != "claude" && value != "codex" {
			issues = append(issues, fmt.Sprintf("%s passes vendor command %q", path, value))
		}
	}
	return issues
}

func requiredIsolatedPackages(facts map[string]*packageFacts) []string {
	var required []string
	for importPath, fact := range facts {
		if !fact.hasTests || importPath == modulePath+"/internal/testenv" {
			continue
		}
		if reachesRisk(importPath, facts, make(map[string]bool)) {
			required = append(required, importPath)
		}
	}
	sort.Strings(required)
	return required
}

func reachesRisk(importPath string, facts map[string]*packageFacts, visiting map[string]bool) bool {
	if importPath == modulePath+"/internal/testenv" || visiting[importPath] {
		return false
	}
	fact := facts[importPath]
	if fact == nil {
		return false
	}
	if fact.directRisk {
		return true
	}
	visiting[importPath] = true
	defer delete(visiting, importPath)
	for dependency := range fact.imports {
		if reachesRisk(dependency, facts, visiting) {
			return true
		}
	}
	return false
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeGoFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
