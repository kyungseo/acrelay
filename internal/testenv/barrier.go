package testenv

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type entrySnapshot struct {
	Mode    os.FileMode
	Size    int64
	ModTime int64
	Digest  string
}

type productionSnapshot struct {
	Exists  bool
	Entries map[string]entrySnapshot
}

// ProductionBarrier observes only acrelay's actual default-owned footprint:
// the top-level ~/.acrelay entry set/metadata and handles.json content digest.
// It never stores or reports raw handle-store content.
type ProductionBarrier struct {
	dir    string
	before productionSnapshot
}

func NewProductionBarrier(dir string) (*ProductionBarrier, error) {
	snapshot, err := snapshotProductionDir(dir)
	if err != nil {
		return nil, err
	}
	return &ProductionBarrier{dir: dir, before: snapshot}, nil
}

func (b *ProductionBarrier) Violations() []string {
	after, err := snapshotProductionDir(b.dir)
	if err != nil {
		return []string{fmt.Sprintf("production barrier could not inspect %s: %v", b.dir, err)}
	}
	var violations []string
	if b.before.Exists != after.Exists {
		if after.Exists {
			return []string{"test created production .acrelay directory"}
		}
		return []string{"test removed production .acrelay directory"}
	}
	if !b.before.Exists {
		return nil
	}

	names := make(map[string]struct{}, len(b.before.Entries)+len(after.Entries))
	for name := range b.before.Entries {
		names[name] = struct{}{}
	}
	for name := range after.Entries {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	for _, name := range ordered {
		oldEntry, oldOK := b.before.Entries[name]
		newEntry, newOK := after.Entries[name]
		switch {
		case !oldOK:
			violations = append(violations, fmt.Sprintf("test created production .acrelay entry %q", name))
		case !newOK:
			violations = append(violations, fmt.Sprintf("test removed production .acrelay entry %q", name))
		case oldEntry.Mode != newEntry.Mode || oldEntry.Size != newEntry.Size || oldEntry.ModTime != newEntry.ModTime:
			violations = append(violations, fmt.Sprintf("test modified production .acrelay entry metadata %q", name))
		case name == "handles.json" && oldEntry.Digest != newEntry.Digest:
			violations = append(violations, "test modified production .acrelay handles.json content")
		}
	}
	return violations
}

func snapshotProductionDir(dir string) (productionSnapshot, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return productionSnapshot{}, nil
	}
	if err != nil {
		return productionSnapshot{}, err
	}
	snapshot := productionSnapshot{Exists: true, Entries: make(map[string]entrySnapshot, len(entries))}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return productionSnapshot{}, err
		}
		item := entrySnapshot{Mode: info.Mode(), Size: info.Size(), ModTime: info.ModTime().UnixNano()}
		if entry.Name() == "handles.json" && info.Mode().IsRegular() {
			digest, err := fileDigest(path)
			if err != nil {
				return productionSnapshot{}, err
			}
			item.Digest = digest
		}
		snapshot.Entries[entry.Name()] = item
	}
	return snapshot, nil
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
