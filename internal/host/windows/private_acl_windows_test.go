//go:build windows

package windows

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	syswindows "golang.org/x/sys/windows"
)

func TestWindowsPrivateACLNestedRoundTrip(t *testing.T) {
	platform := NewWindowsFrontendPlatform()
	parent := filepath.Join(t.TempDir(), "private")
	for _, path := range []string{parent, filepath.Join(parent, "helpers"), filepath.Join(parent, "helpers", "version")} {
		if err := platform.EnsurePrivateDirectory(t.Context(), path); err != nil {
			t.Fatal(err)
		}
		if err := verifyPrivateACL(path, true); err != nil {
			t.Fatalf("nested private directory rejected: %v", err)
		}
		// Exercise the same protected/inherited descriptor on a repair/reuse.
		if err := platform.EnsurePrivateDirectory(t.Context(), path); err != nil {
			t.Fatal(err)
		}
		if err := verifyPrivateACL(path, true); err != nil {
			t.Fatalf("repaired private directory rejected: %v", err)
		}
		file := filepath.Join(path, "state.json")
		if err := platform.WriteProtectedAtomic(t.Context(), file, []byte("{}\n")); err != nil {
			t.Fatal(err)
		}
		if err := verifyPrivateACL(file, false); err != nil {
			t.Fatalf("private file rejected: %v", err)
		}
	}
}

func TestWindowsPrivateACLSplitScopesAndForeignPrincipal(t *testing.T) {
	user, err := syswindows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "split")
	if err = os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// Keep effective full control while explicitly representing the inheritable
	// generic rule separately. This is the documented Windows split-ACE shape.
	sddl := fmt.Sprintf("D:P(A;;FA;;;%s)(A;OICIIO;GA;;;%s)(A;;FA;;;SY)(A;OICIIO;GA;;;SY)",
		user.User.Sid.String(), user.User.Sid.String())
	setPrivateACLTestSDDL(t, path, sddl)
	if err = verifyPrivateACL(path, true); err != nil {
		t.Fatalf("split protected descriptor rejected: %v", err)
	}
	setPrivateACLTestSDDL(t, path, sddl+"(A;OICI;FA;;;BA)")
	if err = verifyPrivateACL(path, true); err == nil {
		t.Fatal("foreign Administrators grant accepted")
	}
}

func setPrivateACLTestSDDL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := syswindows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = syswindows.SetNamedSecurityInfo(path, syswindows.SE_FILE_OBJECT,
		syswindows.DACL_SECURITY_INFORMATION|syswindows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}
