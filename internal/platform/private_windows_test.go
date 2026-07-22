//go:build windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows DACL fixtures (R1-CX-F2 recommendation 6): these run on the
// Windows CI/UTM lanes and pin the creation-time protected-DACL contract —
// pass for the private shape, fail-closed for every other shape, and
// existing weak files are never opened, truncated, or written.

func createWithSDDL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE,
		uint32(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE), sa,
		windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(h)
}

func currentUserSID(t *testing.T) string {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

func TestWindowsPrivateDACLShapes(t *testing.T) {
	me := currentUserSID(t)
	dir := t.TempDir()

	t.Run("protected current-user file and dir pass", func(t *testing.T) {
		path := filepath.Join(dir, "good.bin")
		f, err := OpenPrivateFile(path, os.O_CREATE|os.O_WRONLY)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if err := VerifyPrivateFile(path); err != nil {
			t.Fatalf("created private file must verify: %v", err)
		}
		sub := filepath.Join(dir, "gooddir")
		if err := MkdirPrivate(sub); err != nil {
			t.Fatal(err)
		}
		if err := VerifyPrivateDir(sub); err != nil {
			t.Fatalf("created private dir must verify: %v", err)
		}
	})

	t.Run("plain-created file rejected everywhere", func(t *testing.T) {
		path := filepath.Join(dir, "weak.bin")
		if err := os.WriteFile(path, []byte("secret-free"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := VerifyPrivateFile(path); err == nil {
			t.Fatal("inherited-DACL file must fail verification")
		}
		if _, err := OpenPrivateFile(path, os.O_WRONLY|os.O_CREATE); err == nil {
			t.Fatal("existing weak-DACL file must not be opened for writing")
		}
		if err := WritePrivateFile(path, []byte("sensitive")); err == nil {
			t.Fatal("WritePrivateFile must refuse an existing weak-DACL file")
		}
		got, _ := os.ReadFile(path)
		if string(got) != "secret-free" {
			t.Fatalf("refused write must leave original bytes intact, got %q", got)
		}
	})

	t.Run("non-private SDDL shapes rejected", func(t *testing.T) {
		cases := []struct {
			name string
			sddl string
		}{
			{"unprotected dacl", fmt.Sprintf("D:(A;;FA;;;%s)", me)},
			{"other principal allow", "D:P(A;;FA;;;WD)"},
			// Any non-basic-allow ACE type exercises the same v1 rejection
			// branch as object/callback/unknown types.
			{"non-allow ace type present", fmt.Sprintf("D:P(D;;FA;;;WD)(A;;FA;;;%s)", me)},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				path := filepath.Join(dir, "sddl-"+tc.name)
				createWithSDDL(t, path, tc.sddl)
				if err := VerifyPrivateFile(path); err == nil {
					t.Fatalf("shape %q must fail verification", tc.sddl)
				}
				if _, err := OpenPrivateFile(path, os.O_WRONLY); err == nil {
					t.Fatalf("shape %q must not open", tc.sddl)
				}
			})
		}
	})

	t.Run("rename preserves protection", func(t *testing.T) {
		src := filepath.Join(dir, "moved-src.bin")
		dst := filepath.Join(dir, "moved-dst.bin")
		f, err := OpenPrivateFile(src, os.O_CREATE|os.O_WRONLY)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		if err := os.Rename(src, dst); err != nil {
			t.Fatal(err)
		}
		if err := VerifyPrivateFile(dst); err != nil {
			t.Fatalf("rename must preserve the protected DACL: %v", err)
		}
	})

	t.Run("existing permissive dir not reused", func(t *testing.T) {
		weak := filepath.Join(dir, "weakdir")
		if err := os.Mkdir(weak, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := MkdirPrivate(weak); err == nil {
			t.Fatal("MkdirPrivate must refuse an existing non-private directory")
		}
	})

	t.Run("temp candidates are exclusive", func(t *testing.T) {
		a, err := MkdirTempPrivate("acrelay-dacl-fixture-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(a)
		b, err := MkdirTempPrivate("acrelay-dacl-fixture-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(b)
		if a == b {
			t.Fatal("temp directories must be exclusively created, never reused")
		}
		if err := VerifyPrivateDir(a); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("rejected open flags", func(t *testing.T) {
		if _, err := OpenPrivateFile(filepath.Join(dir, "append.bin"), os.O_CREATE|os.O_WRONLY|os.O_APPEND); err == nil {
			t.Fatal("O_APPEND is outside the v1 allowlist and must be rejected")
		}
	})
}
