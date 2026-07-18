package adapter

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// HandleStore maps opaque random session references to native vendor resume
// handles. The file is private storage: 0600 from creation, unique-temp
// atomic replace, merge-preserving writes. References are cryptographically
// random — never derived from the native handle (DR-811 §3).
type HandleStore struct {
	Path string
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

// load reads the store. A missing file yields an empty store; a corrupt or
// version-mismatched file fails closed — it is never silently recreated.
func (h *HandleStore) load() (*handleFile, error) {
	b, err := os.ReadFile(h.Path)
	if os.IsNotExist(err) {
		return &handleFile{Version: handleFileVersion, Entries: map[string]handleEntry{}}, nil
	}
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

// Register stores a native handle under a fresh random reference and
// returns the reference. Existing entries are preserved.
func (h *HandleStore) Register(vendor, nativeHandle string) (string, error) {
	f, err := h.load()
	if err != nil {
		return "", err
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ref := "sref-" + hex.EncodeToString(b)
	f.Entries[ref] = handleEntry{Vendor: vendor, Handle: nativeHandle}
	if err := h.save(f); err != nil {
		return "", err
	}
	return ref, nil
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

// Rotate replaces the native handle behind a reference with a new handle
// under a NEW reference (session reset semantics) and removes the old
// reference. The caller records the reset reason in the canonical record.
func (h *HandleStore) Rotate(oldRef, vendor, newHandle string) (string, error) {
	f, err := h.load()
	if err != nil {
		return "", err
	}
	if _, ok := f.Entries[oldRef]; !ok {
		return "", fmt.Errorf("session_ref %s not found for rotation: fail-closed", oldRef)
	}
	delete(f.Entries, oldRef)
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ref := "sref-" + hex.EncodeToString(b)
	f.Entries[ref] = handleEntry{Vendor: vendor, Handle: newHandle}
	if err := h.save(f); err != nil {
		return "", err
	}
	return ref, nil
}

// Delete removes a reference (collaboration retention cleanup).
func (h *HandleStore) Delete(ref string) error {
	f, err := h.load()
	if err != nil {
		return err
	}
	if _, ok := f.Entries[ref]; !ok {
		return fmt.Errorf("session_ref %s not found for deletion", ref)
	}
	delete(f.Entries, ref)
	return h.save(f)
}
