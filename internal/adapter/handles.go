package adapter

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kyungseo/acrelay/internal/platform"
	"github.com/kyungseo/acrelay/internal/store"
)

// HandleStore maps opaque random session references to native vendor resume
// handles. The file is private storage: 0600 from creation, owner-only
// permission verified on every load, unique-temp atomic replace, and
// merge-preserving writes. References are 128-bit cryptographically random
// values — never derived from the native handle (DR-811 §3).
//
// Mutations are serialized across processes by an exclusive advisory lock on
// a sidecar lock file: a concurrent writer blocks for the (millisecond-scale)
// critical section instead of losing the other writer's update. The claim
// boundary is serialization, not lock-free CAS (R1-CX-F4).
type HandleStore struct {
	Path string
}

// withExclusiveLock runs fn while holding an exclusive flock on the sidecar
// lock file. The lock is advisory but every mutation path in this package
// goes through it, so two acrelay processes can never interleave
// load→mutate→save and drop each other's entries.
func (h *HandleStore) withExclusiveLock(fn func() error) error {
	fd, err := platform.OpenPrivateFile(h.Path+".lock", os.O_CREATE|os.O_WRONLY)
	if err != nil {
		return err
	}
	defer fd.Close()
	if err := platform.LockExclusive(fd); err != nil {
		return fmt.Errorf("handle store lock failed: %w", err)
	}
	defer platform.Unlock(fd)
	if afterLockAcquired != nil {
		afterLockAcquired()
	}
	return fn()
}

// afterLockAcquired is a hook invoked while holding the mutation lock. It is
// nil in production — no env lookup, no delay on the real path — and is set
// only by concurrency tests to widen the critical section (R2-F2).
var afterLockAcquired func()

// beforeHandleCleanupSave is a deterministic crash-window hook used only by
// package tests. Production leaves it nil.
var beforeHandleCleanupSave func() error

type handleFile struct {
	Version int                    `json:"version"`
	Entries map[string]handleEntry `json:"entries"`
}

type handleEntry struct {
	Vendor     string `json:"vendor"`
	Handle     string `json:"handle"`
	ProfileID  string `json:"profile_id"`
	WorkingDir string `json:"working_dir"`
}

const handleFileVersion = 2
const legacyHandleFileVersion = 1

const neutralRuntimeDir = "runtime"

// HandleInfo is the non-secret lifecycle view of one opaque reference. The
// native vendor handle is deliberately excluded from diagnostics and CLI
// output.
type HandleInfo struct {
	Ref            string
	Vendor         string
	ProfileID      string
	WorkingDir     string
	Neutral        bool
	LegacyTempRoot bool
	WorkingDirGone bool
}

func platformNeutralRuntimeRoot() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("durable private runtime root unavailable: %w", err)
	}
	return filepath.Join(dir, "acrelay", neutralRuntimeDir), nil
}

// NeutralRuntimeRoot returns the durable cwd namespace paired with this
// handle store. An empty store path uses the platform config fallback. Path
// resolution never moves an existing handle or cwd.
func (h *HandleStore) NeutralRuntimeRoot() (string, error) {
	if strings.TrimSpace(h.Path) == "" {
		return platformNeutralRuntimeRoot()
	}
	dir, err := filepath.Abs(filepath.Dir(h.Path))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, neutralRuntimeDir), nil
}

func directNeutralChild(root, path string) bool {
	cleanRoot, cleanPath := filepath.Clean(root), filepath.Clean(path)
	return filepath.IsAbs(cleanPath) && filepath.Dir(cleanPath) == cleanRoot &&
		strings.HasPrefix(filepath.Base(cleanPath), "acrelay-review-root-")
}

func legacyTempNeutralPath(path string) bool {
	clean := filepath.Clean(path)
	return filepath.IsAbs(clean) && filepath.Dir(clean) == filepath.Clean(os.TempDir()) &&
		strings.HasPrefix(filepath.Base(clean), "acrelay-review-root-")
}

func (h *HandleStore) classifyNeutralPath(path string) (durable, legacy bool, err error) {
	root, err := h.NeutralRuntimeRoot()
	if err != nil {
		return false, false, err
	}
	durable = directNeutralChild(root, path)
	if fallback, fallbackErr := platformNeutralRuntimeRoot(); fallbackErr == nil {
		durable = durable || directNeutralChild(fallback, path)
	}
	return durable, legacyTempNeutralPath(path), nil
}

// load reads the store. A missing file yields an empty store; a corrupt,
// version-mismatched, or group/other-accessible file fails closed — it is
// never silently recreated or repaired.
func (h *HandleStore) load() (*handleFile, error) {
	_, err := os.Stat(h.Path)
	if os.IsNotExist(err) {
		return &handleFile{Version: handleFileVersion, Entries: map[string]handleEntry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if err := platform.VerifyPrivateFile(h.Path); err != nil {
		return nil, fmt.Errorf("handle store %s %v: fail-closed", h.Path, err)
	}
	b, err := os.ReadFile(h.Path)
	if err != nil {
		return nil, err
	}
	var f handleFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("handle store corrupt (%s): fail-closed, refusing to overwrite: %w", h.Path, err)
	}
	if f.Version != handleFileVersion {
		return nil, fmt.Errorf("handle store version %d unsupported (want %d): fail-closed", f.Version, handleFileVersion)
	}
	if f.Entries == nil {
		f.Entries = map[string]handleEntry{}
	}
	return &f, nil
}

// PrepareForDispatch validates the private handle store before reviewer
// execution. A v1 store cannot preserve resume safety because its entries did
// not bind a trust profile or working directory. For a fresh reviewer session,
// acRelay therefore preserves the exact v1 bytes in a private backup and
// starts an empty v2 store. A resume attempt fails closed and asks for an
// explicit session reset instead of silently discarding continuity.
func (h *HandleStore) PrepareForDispatch(resumeRef string) (string, error) {
	diagnostic := ""
	err := h.withExclusiveLock(func() error {
		b, err := os.ReadFile(h.Path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := platform.VerifyPrivateFile(h.Path); err != nil {
			return fmt.Errorf("handle store %s %v: fail-closed", h.Path, err)
		}
		var header struct {
			Version int `json:"version"`
		}
		if err := json.Unmarshal(b, &header); err != nil {
			return fmt.Errorf("handle store corrupt (%s): fail-closed, refusing to overwrite: %w", h.Path, err)
		}
		switch header.Version {
		case handleFileVersion:
			_, err := h.load()
			return err
		case legacyHandleFileVersion:
			var legacy struct {
				Version int `json:"version"`
				Entries map[string]struct {
					Vendor string `json:"vendor"`
					Handle string `json:"handle"`
				} `json:"entries"`
			}
			if err := json.Unmarshal(b, &legacy); err != nil {
				return fmt.Errorf("legacy handle store corrupt (%s): fail-closed: %w", h.Path, err)
			}
			for ref, entry := range legacy.Entries {
				if strings.TrimSpace(ref) == "" || strings.TrimSpace(entry.Vendor) == "" || strings.TrimSpace(entry.Handle) == "" {
					return fmt.Errorf("legacy handle store contains an incomplete entry: fail-closed")
				}
			}
			if strings.TrimSpace(resumeRef) != "" {
				return fmt.Errorf("session_ref %s belongs to legacy handle store v1 and cannot be resumed safely: use an explicit session reset; review not started",
					resumeRef)
			}
			backup := h.Path + ".v1.backup"
			if existing, readErr := os.ReadFile(backup); readErr == nil {
				if err := platform.VerifyPrivateFile(backup); err != nil {
					return fmt.Errorf("legacy handle backup %s %v: fail-closed", backup, err)
				}
				if !bytes.Equal(existing, b) {
					return fmt.Errorf("legacy handle backup %s already exists with different content: fail-closed", backup)
				}
			} else if !os.IsNotExist(readErr) {
				return readErr
			} else if _, err := store.WritePrivateAtomic(backup, b); err != nil {
				return fmt.Errorf("preserve legacy handle backup: %w", err)
			}
			if _, err := store.WritePrivateAtomic(h.Path, []byte("{\n \"version\": 2,\n \"entries\": {}\n}")); err != nil {
				return fmt.Errorf("initialize handle store v2 after backup: %w", err)
			}
			diagnostic = fmt.Sprintf("legacy handle store v1 preserved at %s; fresh v2 store initialized", backup)
			return nil
		default:
			return fmt.Errorf("handle store version %d unsupported (want %d): fail-closed", header.Version, handleFileVersion)
		}
	})
	return diagnostic, err
}

func (h *HandleStore) save(f *handleFile) error {
	b, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	_, err = store.WritePrivateAtomic(h.Path, b)
	return err
}

func newRef(existing map[string]handleEntry) (string, error) {
	for i := 0; i < 5; i++ { // collision retry — 128-bit space makes >1 loop cosmically unlikely
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		ref := "sref-" + hex.EncodeToString(b)
		if _, taken := existing[ref]; !taken {
			return ref, nil
		}
	}
	return "", fmt.Errorf("session_ref collision retry exhausted: fail-closed")
}

// Native handle format contract (FEAT-20260721-002, R0-CX-F1): a persisted
// handle reaches the vendor CLI argv verbatim, so a malformed entry is
// rejected fail-closed before any child start — never dropped, truncated, or
// silently replaced by a new session. The Claude session_id shape is the
// observed UUID form (Claude Code 2.1.215; re-observed on 2.1.217 during the
// FEAT-20260722-001 restriction re-verification spike); format drift fails
// closed like every other version-bound observation. Codex thread IDs use a
// conservative argv-safe charset with the same no-leading-option rule.
var claudeHandlePattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var codexHandlePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{7,127}$`)

// ValidateNativeHandle enforces the vendor-safe handle format. Unknown
// vendors get the conservative argv-safe rule.
func ValidateNativeHandle(vendor, handle string) error {
	if len(handle) < 8 || len(handle) > 128 {
		return fmt.Errorf("%s native handle length %d outside the 8..128 contract: fail-closed (no child start)", vendor, len(handle))
	}
	for _, r := range handle {
		if r < 0x21 || r > 0x7e {
			return fmt.Errorf("%s native handle contains whitespace, control, or non-ASCII bytes: fail-closed (no child start)", vendor)
		}
	}
	if strings.HasPrefix(handle, "-") {
		return fmt.Errorf("%s native handle starts with an option prefix: fail-closed (no child start)", vendor)
	}
	switch vendor {
	case "claude":
		if !claudeHandlePattern.MatchString(handle) {
			return fmt.Errorf("claude native handle does not match the observed session_id format: fail-closed (no child start)")
		}
	default:
		if !codexHandlePattern.MatchString(handle) {
			return fmt.Errorf("%s native handle contains argv-unsafe characters: fail-closed (no child start)", vendor)
		}
	}
	return nil
}

func validateEntry(vendor, nativeHandle, profileID, workingDir string) error {
	if strings.TrimSpace(vendor) == "" || strings.TrimSpace(nativeHandle) == "" || strings.TrimSpace(profileID) == "" || strings.TrimSpace(workingDir) == "" {
		return fmt.Errorf("vendor, native handle, trust profile, and working directory must be nonempty: fail-closed")
	}
	if err := ValidateNativeHandle(vendor, nativeHandle); err != nil {
		return err
	}
	if !filepath.IsAbs(workingDir) {
		return fmt.Errorf("handle working directory must be absolute: fail-closed")
	}
	return nil
}

func (h *HandleStore) validateEntry(vendor, nativeHandle, profileID, workingDir string) error {
	if err := validateEntry(vendor, nativeHandle, profileID, workingDir); err != nil {
		return err
	}
	if strings.HasSuffix(profileID, "/"+WorkingDirNeutral) {
		durable, legacy, err := h.classifyNeutralPath(workingDir)
		if err != nil {
			return err
		}
		if !durable && !legacy {
			return fmt.Errorf("neutral handle working directory is outside the acrelay-owned durable or legacy temp namespace: fail-closed")
		}
	}
	return nil
}

// Register stores a native handle under a fresh random reference and
// returns the reference. Existing entries are preserved.
func (h *HandleStore) Register(vendor, nativeHandle, profileID, workingDir string) (string, error) {
	if err := h.validateEntry(vendor, nativeHandle, profileID, workingDir); err != nil {
		return "", err
	}
	var ref string
	err := h.withExclusiveLock(func() error {
		f, err := h.load()
		if err != nil {
			return err
		}
		r, err := newRef(f.Entries)
		if err != nil {
			return err
		}
		f.Entries[r] = handleEntry{Vendor: vendor, Handle: nativeHandle, ProfileID: profileID, WorkingDir: workingDir}
		if err := h.save(f); err != nil {
			return err
		}
		ref = r
		return nil
	})
	return ref, err
}

// Lookup resolves a reference. A missing reference fails closed — the
// caller must never silently fall back to a new session (DR-811 §1).
func (h *HandleStore) Lookup(ref string) (vendor, nativeHandle, profileID, workingDir string, err error) {
	f, err := h.load()
	if err != nil {
		return "", "", "", "", err
	}
	e, ok := f.Entries[ref]
	if !ok {
		return "", "", "", "", fmt.Errorf("session_ref %s not found: fail-closed (no silent new-session fallback)", ref)
	}
	if err := h.validateEntry(e.Vendor, e.Handle, e.ProfileID, e.WorkingDir); err != nil {
		return "", "", "", "", fmt.Errorf("session_ref %s invalid: %w", ref, err)
	}
	return e.Vendor, e.Handle, e.ProfileID, e.WorkingDir, nil
}

// Rotate replaces the native handle behind a reference with a new handle of
// the SAME vendor under a new reference (session reset semantics) and
// removes the old reference. The caller records the reset reason in the
// canonical record.
func (h *HandleStore) Rotate(oldRef, vendor, newHandle, profileID string) (string, error) {
	var ref string
	err := h.withExclusiveLock(func() error {
		f, err := h.load()
		if err != nil {
			return err
		}
		old, ok := f.Entries[oldRef]
		if !ok {
			return fmt.Errorf("session_ref %s not found for rotation: fail-closed", oldRef)
		}
		if old.Vendor != vendor {
			return fmt.Errorf("rotation vendor mismatch: ref %s belongs to %s, not %s: fail-closed", oldRef, old.Vendor, vendor)
		}
		if old.ProfileID != profileID {
			return fmt.Errorf("rotation trust profile mismatch: ref %s belongs to %s, not %s: fail-closed", oldRef, old.ProfileID, profileID)
		}
		if err := h.validateEntry(vendor, newHandle, profileID, old.WorkingDir); err != nil {
			return err
		}
		delete(f.Entries, oldRef)
		r, err := newRef(f.Entries)
		if err != nil {
			return err
		}
		f.Entries[r] = handleEntry{Vendor: vendor, Handle: newHandle, ProfileID: profileID, WorkingDir: old.WorkingDir}
		if err := h.save(f); err != nil {
			return err
		}
		ref = r
		return nil
	})
	return ref, err
}

// Inspect returns a bounded lifecycle view without exposing the native vendor
// handle. A missing cwd is diagnostic state, not an automatic migration.
func (h *HandleStore) Inspect(ref string) (HandleInfo, bool, error) {
	f, err := h.load()
	if err != nil {
		return HandleInfo{}, false, err
	}
	e, ok := f.Entries[ref]
	if !ok {
		return HandleInfo{Ref: ref}, false, nil
	}
	if err := h.validateEntry(e.Vendor, e.Handle, e.ProfileID, e.WorkingDir); err != nil {
		return HandleInfo{}, true, fmt.Errorf("session_ref %s invalid: %w", ref, err)
	}
	neutral := strings.HasSuffix(e.ProfileID, "/"+WorkingDirNeutral)
	_, legacy, err := h.classifyNeutralPath(e.WorkingDir)
	if err != nil {
		return HandleInfo{}, true, err
	}
	_, statErr := os.Lstat(e.WorkingDir)
	return HandleInfo{
		Ref: ref, Vendor: e.Vendor, ProfileID: e.ProfileID, WorkingDir: e.WorkingDir,
		Neutral: neutral, LegacyTempRoot: neutral && legacy, WorkingDirGone: os.IsNotExist(statErr),
	}, true, nil
}

// Cleanup removes an exact reference and its acrelay-owned neutral cwd. It is
// idempotent for an already-absent ref. Cwd deletion happens before the atomic
// store mutation: a crash may leave a retained ref pointing at a missing cwd,
// which is diagnosable and converges on retry; cross-resource atomicity is not
// claimed.
func (h *HandleStore) Cleanup(ref string) (HandleInfo, bool, error) {
	var removed HandleInfo
	var found bool
	err := h.withExclusiveLock(func() error {
		f, err := h.load()
		if err != nil {
			return err
		}
		e, ok := f.Entries[ref]
		if !ok {
			return nil
		}
		if err := h.validateEntry(e.Vendor, e.Handle, e.ProfileID, e.WorkingDir); err != nil {
			return fmt.Errorf("refusing cleanup for invalid session_ref %s: %w", ref, err)
		}
		found = true
		neutral := strings.HasSuffix(e.ProfileID, "/"+WorkingDirNeutral)
		_, legacy, err := h.classifyNeutralPath(e.WorkingDir)
		if err != nil {
			return err
		}
		removed = HandleInfo{Ref: ref, Vendor: e.Vendor, ProfileID: e.ProfileID, WorkingDir: e.WorkingDir, Neutral: neutral, LegacyTempRoot: neutral && legacy}
		if neutral {
			if st, statErr := os.Lstat(e.WorkingDir); statErr == nil {
				if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
					return fmt.Errorf("refusing neutral cwd cleanup %q: not a plain directory", e.WorkingDir)
				}
				if err := platform.VerifyPrivateDir(e.WorkingDir); err != nil {
					return fmt.Errorf("refusing neutral cwd cleanup %q: %w", e.WorkingDir, err)
				}
				if err := os.RemoveAll(filepath.Clean(e.WorkingDir)); err != nil {
					return err
				}
			} else if !os.IsNotExist(statErr) {
				return statErr
			} else {
				removed.WorkingDirGone = true
			}
		}
		if beforeHandleCleanupSave != nil {
			if err := beforeHandleCleanupSave(); err != nil {
				return err
			}
		}
		delete(f.Entries, ref)
		return h.save(f)
	})
	return removed, found, err
}

// Delete preserves the original strict missing-ref behavior for internal
// callers; lifecycle cleanup uses Cleanup for idempotent convergence.
func (h *HandleStore) Delete(ref string) error {
	_, found, err := h.Cleanup(ref)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("session_ref %s not found for deletion", ref)
	}
	return nil
}
