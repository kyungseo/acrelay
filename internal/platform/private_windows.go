//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Private artifacts on Windows carry a creation-time protected DACL whose
// only explicit allow ACE is the current user SID (v1 principal allowlist —
// FEAT-20260722-002 R0/R1). SYSTEM/Administrators ACEs are not added.
// The claim is limited to ordinary cross-user access denial: a privileged
// administrator's ownership takeover or backup privilege is not defended
// against and no such claim is made.
//
// R1-CX-F2 hardening: a CreateFile security descriptor applies only to newly
// created objects, so every open verifies the resulting HANDLE's security —
// an existing file with a weak or inherited DACL fails closed before any
// truncate or write. The verifier requires SE_DACL_PRESENT and
// SE_DACL_PROTECTED and accepts only basic ACCESS_ALLOWED ACEs whose SID is
// the current user; every other ACE type (deny/object/callback/unknown) is
// fail-closed in v1 rather than partially interpreted.

// allowedOpenFlags is the explicit allowlist of os.OpenFile flag bits this
// boundary implements. Anything else (e.g. O_APPEND) is rejected instead of
// being half-translated (R1-CX-F2 recommendation 5).
const allowedOpenFlags = os.O_RDONLY | os.O_WRONLY | os.O_RDWR | os.O_CREATE | os.O_EXCL | os.O_TRUNC

func privateSecurityAttributes() (*windows.SecurityAttributes, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve current user SID: fail-closed: %w", err)
	}
	sddl := fmt.Sprintf("D:P(A;OICI;FA;;;%s)", user.User.Sid.String())
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, fmt.Errorf("build protected DACL: fail-closed: %w", err)
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}, nil
}

// OpenPrivateFile creates or opens path with the protected DACL applied at
// creation time, then verifies the opened handle's security before any
// truncation — existing weak-DACL files are never used or truncated.
func OpenPrivateFile(path string, flag int) (*os.File, error) {
	if flag&^allowedOpenFlags != 0 {
		return nil, fmt.Errorf("open flag %#x outside the private-file allowlist: fail-closed", flag)
	}
	sa, err := privateSecurityAttributes()
	if err != nil {
		return nil, err
	}
	var access uint32
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		access = windows.GENERIC_WRITE
	case os.O_RDWR:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	default:
		access = windows.GENERIC_READ
	}
	access |= windows.READ_CONTROL
	// Truncation is deferred until after handle verification, so the
	// disposition never uses CREATE_ALWAYS/TRUNCATE_EXISTING.
	var disposition uint32
	switch {
	case flag&(os.O_CREATE|os.O_EXCL) == os.O_CREATE|os.O_EXCL:
		disposition = windows.CREATE_NEW
	case flag&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	default:
		disposition = windows.OPEN_EXISTING
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, access,
		uint32(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE), sa,
		disposition, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, fmt.Errorf("create private file %s: fail-closed: %w", path, err)
	}
	f := os.NewFile(uintptr(h), path)
	if err := verifyPrivateHandle(h); err != nil {
		f.Close()
		return nil, fmt.Errorf("private file %s: %w", path, err)
	}
	if flag&os.O_TRUNC != 0 {
		if err := f.Truncate(0); err != nil {
			f.Close()
			return nil, err
		}
		if _, err := f.Seek(0, 0); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// WritePrivateFile writes data to a file that carries the protected DACL.
// The handle is verified before the (deferred) truncation, so sensitive
// bytes never land in a pre-existing weak-DACL file.
func WritePrivateFile(path string, data []byte) error {
	f, err := OpenPrivateFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// mkdirPrivateExclusive creates dir with the protected DACL; an existing
// path is reported as os.ErrExist (never silently reused — R1-CX-F3).
func mkdirPrivateExclusive(dir string) error {
	sa, err := privateSecurityAttributes()
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	if err := windows.CreateDirectory(p, sa); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return os.ErrExist
		}
		return fmt.Errorf("create private directory %s: fail-closed: %w", dir, err)
	}
	return nil
}

// MkdirPrivate creates dir with the protected DACL applied at creation time.
// An existing directory must itself verify as private (fail-closed instead
// of silent reuse); missing parents are created with the same protection.
func MkdirPrivate(dir string) error {
	if st, err := os.Lstat(dir); err == nil {
		if !st.IsDir() {
			return fmt.Errorf("private path %s exists and is not a directory: fail-closed", dir)
		}
		return VerifyPrivateDir(dir)
	}
	parent := filepath.Dir(dir)
	if parent != dir {
		if _, err := os.Lstat(parent); os.IsNotExist(err) {
			if err := MkdirPrivate(parent); err != nil {
				return err
			}
		}
	}
	err := mkdirPrivateExclusive(dir)
	if errors.Is(err, os.ErrExist) {
		// Creation race: the directory appeared concurrently — accept only
		// if it verifies as private.
		return VerifyPrivateDir(dir)
	}
	return err
}

// MkdirTempPrivate creates a fresh protected temporary directory. Candidates
// are created exclusively — an existing directory is never reused as a new
// temp (R1-CX-F3) — and only ERROR_ALREADY_EXISTS triggers a retry.
func MkdirTempPrivate(prefix string) (string, error) {
	base := os.TempDir()
	for i := 0; i < 10000; i++ {
		candidate := filepath.Join(base, prefix+randomSuffix())
		err := mkdirPrivateExclusive(candidate)
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("private temp directory collision retry exhausted: fail-closed")
}

// VerifyPrivateFile fails closed unless the existing file's security is the
// protected current-user-only shape.
func VerifyPrivateFile(path string) error {
	return verifyPrivatePath(path, false)
}

// VerifyPrivateDir fails closed unless path is a non-symlink directory whose
// security is the protected current-user-only shape.
func VerifyPrivateDir(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("private directory unavailable: fail-closed: %w", err)
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return fmt.Errorf("directory %s is not a plain directory: fail-closed", path)
	}
	return verifyPrivatePath(path, true)
}

// verifyPrivatePath opens the object for security inspection and verifies
// the handle, so the verified object is exactly the one opened.
func verifyPrivatePath(path string, isDir bool) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var attrs uint32 = windows.FILE_ATTRIBUTE_NORMAL
	if isDir {
		attrs = windows.FILE_FLAG_BACKUP_SEMANTICS // required to open directories
	}
	h, err := windows.CreateFile(p, windows.READ_CONTROL,
		uint32(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE), nil,
		windows.OPEN_EXISTING, attrs, 0)
	if err != nil {
		return fmt.Errorf("open %s for security inspection: fail-closed: %w", path, err)
	}
	defer windows.CloseHandle(h)
	if err := verifyPrivateHandle(h); err != nil {
		return fmt.Errorf("private object %s: %w", path, err)
	}
	return nil
}

// verifyPrivateHandle checks the handle's security descriptor: DACL present
// and protected (no inheritance), and every ACE is a basic ACCESS_ALLOWED
// ACE for the current user SID. Any other ACE type — deny, object, callback,
// or unknown — fails closed in v1 (R1-CX-F2 recommendation 4).
func verifyPrivateHandle(h windows.Handle) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("read security descriptor: fail-closed: %w", err)
	}
	control, _, err := sd.Control()
	if err != nil {
		return fmt.Errorf("read SD control: fail-closed: %w", err)
	}
	if control&windows.SE_DACL_PRESENT == 0 {
		return fmt.Errorf("no DACL present (unrestricted object): fail-closed")
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return fmt.Errorf("DACL is not protected (inheritance not blocked; control=%#x): fail-closed", control)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return fmt.Errorf("no readable DACL: fail-closed")
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("resolve current user SID: fail-closed: %w", err)
	}
	if dacl.AceCount == 0 {
		return fmt.Errorf("empty DACL grants nothing but is not the expected private shape: fail-closed")
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read ACE %d: fail-closed: %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("ACE %d has type %d (only basic current-user allow ACEs are accepted in v1): fail-closed", i, ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.Equals(user.User.Sid) {
			return fmt.Errorf("ACE %d grants access to another principal (%s): fail-closed", i, sid.String())
		}
	}
	return nil
}
