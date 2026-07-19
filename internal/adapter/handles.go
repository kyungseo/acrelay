package adapter

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
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
	fd, err := os.OpenFile(h.Path+".lock", os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer fd.Close()
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("handle store lock failed: fail-closed, refusing unserialized mutation: %w", err)
	}
	defer syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)
	inLockTestDelay() // no-op unless ACRELAY_TEST_LOCK_DELAY_MS is set (test-only)
	return fn()
}

// inLockTestDelay widens the mutation critical section for concurrency tests
// so overlapping processes provably contend inside the lock, not merely at
// the barrier. It is inert in production: the env var is never set there.
func inLockTestDelay() {
	ms := os.Getenv("ACRELAY_TEST_LOCK_DELAY_MS")
	if ms == "" {
		return
	}
	if d, err := strconv.Atoi(ms); err == nil && d > 0 {
		time.Sleep(time.Duration(d) * time.Millisecond)
	}
}

type handleFile struct {
	Version int                    `json:"version"`
	Entries map[string]handleEntry `json:"entries"`
}

type handleEntry struct {
	Vendor string `json:"vendor"`
	Handle string `json:"handle"`
}

const handleFileVersion = 1

// load reads the store. A missing file yields an empty store; a corrupt,
// version-mismatched, or group/other-accessible file fails closed — it is
// never silently recreated or repaired.
func (h *HandleStore) load() (*handleFile, error) {
	st, err := os.Stat(h.Path)
	if os.IsNotExist(err) {
		return &handleFile{Version: handleFileVersion, Entries: map[string]handleEntry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("handle store %s permission %o exposes group/other: fail-closed", h.Path, st.Mode().Perm())
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

func (h *HandleStore) save(f *handleFile) error {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(h.Path),
		fmt.Sprintf(".%s.tmp-%d-%s", filepath.Base(h.Path), os.Getpid(), hex.EncodeToString(suffix)))
	fd, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // 0600 from creation
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	b, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		fd.Close()
		return err
	}
	if _, err := fd.Write(b); err != nil {
		fd.Close()
		return err
	}
	if err := fd.Sync(); err != nil {
		fd.Close()
		return err
	}
	if err := fd.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, h.Path)
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

func validateEntry(vendor, nativeHandle string) error {
	if strings.TrimSpace(vendor) == "" || strings.TrimSpace(nativeHandle) == "" {
		return fmt.Errorf("vendor and native handle must be nonempty: fail-closed (empty handles are never stored)")
	}
	return nil
}

// Register stores a native handle under a fresh random reference and
// returns the reference. Existing entries are preserved.
func (h *HandleStore) Register(vendor, nativeHandle string) (string, error) {
	if err := validateEntry(vendor, nativeHandle); err != nil {
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
		f.Entries[r] = handleEntry{Vendor: vendor, Handle: nativeHandle}
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
func (h *HandleStore) Lookup(ref string) (vendor, nativeHandle string, err error) {
	f, err := h.load()
	if err != nil {
		return "", "", err
	}
	e, ok := f.Entries[ref]
	if !ok {
		return "", "", fmt.Errorf("session_ref %s not found: fail-closed (no silent new-session fallback)", ref)
	}
	return e.Vendor, e.Handle, nil
}

// Rotate replaces the native handle behind a reference with a new handle of
// the SAME vendor under a new reference (session reset semantics) and
// removes the old reference. The caller records the reset reason in the
// canonical record.
func (h *HandleStore) Rotate(oldRef, vendor, newHandle string) (string, error) {
	if err := validateEntry(vendor, newHandle); err != nil {
		return "", err
	}
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
		delete(f.Entries, oldRef)
		r, err := newRef(f.Entries)
		if err != nil {
			return err
		}
		f.Entries[r] = handleEntry{Vendor: vendor, Handle: newHandle}
		if err := h.save(f); err != nil {
			return err
		}
		ref = r
		return nil
	})
	return ref, err
}

// Delete removes a reference (collaboration retention cleanup).
func (h *HandleStore) Delete(ref string) error {
	return h.withExclusiveLock(func() error {
		f, err := h.load()
		if err != nil {
			return err
		}
		if _, ok := f.Entries[ref]; !ok {
			return fmt.Errorf("session_ref %s not found for deletion", ref)
		}
		delete(f.Entries, ref)
		return h.save(f)
	})
}
