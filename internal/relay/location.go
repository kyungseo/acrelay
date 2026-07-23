package relay

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// LocationSignal is a bounded, verified indicator that a canonical path is
// inside a known publication/synchronization surface. Absence of a signal is
// never reported as proof that a location is safe.
type LocationSignal struct {
	Kind       string
	Source     string
	Limitation string
}

// LocationOverride is a one-shot init decision. It is recorded in the raw
// canonical objective header and does not become a reusable/global policy.
type LocationOverride struct {
	Actor     string
	Rationale string
}

func resolvedWithin(root, candidate string) (bool, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, err
	}
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

func canonicalParent(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(filepath.Dir(abs))
}

// InspectPrivateLocation checks the minimum supported signal set: a concrete
// VCS ancestor, Windows OneDrive roots explicitly supplied by the platform,
// and the existing macOS iCloud Drive root. It intentionally does not guess
// arbitrary sync tools or claim exhaustive coverage.
func InspectPrivateLocation(path string) ([]LocationSignal, error) {
	parent, err := canonicalParent(path)
	if err != nil {
		return nil, err
	}
	var signals []LocationSignal
	for dir := parent; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			signals = append(signals, LocationSignal{Kind: "vcs-ancestor", Source: ".git ancestor", Limitation: "detects only this VCS marker"})
			break
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect VCS marker: %w", err)
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}

	type syncRoot struct{ kind, source, path string }
	var roots []syncRoot
	for _, key := range []string{"OneDrive", "OneDriveCommercial", "OneDriveConsumer"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" && filepath.IsAbs(value) {
			roots = append(roots, syncRoot{"sync-onedrive", "platform environment " + key, value})
		}
	}
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			roots = append(roots, syncRoot{"sync-icloud-drive", "macOS iCloud Drive root", filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")})
		}
	}
	seen := map[string]bool{}
	for _, root := range roots {
		st, statErr := os.Stat(root.path)
		if statErr != nil || !st.IsDir() {
			continue
		}
		inside, err := resolvedWithin(root.path, parent)
		if err != nil {
			continue
		}
		resolvedRoot, err := filepath.EvalSymlinks(root.path)
		if err != nil {
			continue
		}
		key := root.kind + "\x00" + resolvedRoot
		if inside && !seen[key] {
			seen[key] = true
			signals = append(signals, LocationSignal{Kind: root.kind, Source: root.source, Limitation: "supported root only; other sync providers and aliases are not detected"})
		}
	}
	sort.Slice(signals, func(i, j int) bool {
		if signals[i].Kind == signals[j].Kind {
			return signals[i].Source < signals[j].Source
		}
		return signals[i].Kind < signals[j].Kind
	})
	return signals, nil
}

func locationSignalKinds(signals []LocationSignal) string {
	kinds := make([]string, 0, len(signals))
	for _, signal := range signals {
		kinds = append(kinds, signal.Kind)
	}
	return strings.Join(kinds, ",")
}
