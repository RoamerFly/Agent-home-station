//go:build windows

package devicebroker

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestPrivateLeaseDirectoryWindowsDACL(t *testing.T) {
	path := t.TempDir() + `\device-broker-leases`
	actual, err := ensurePrivateDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := windows.GetNamedSecurityInfo(actual, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		t.Fatalf("owner is not current user: %v", err)
	}
	sddl := descriptor.String()
	if !strings.Contains(sddl, "D:P") || !strings.Contains(sddl, ";;;"+user.String()+")") || !strings.Contains(sddl, ";;;SY)") || strings.Count(sddl, "(A;") != 2 {
		t.Fatalf("unexpected private DACL: %s", sddl)
	}
	// This simulates an existing directory owned by another principal without
	// requiring rights to create a real second Windows user in the test run.
	other, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ensurePrivateDirectoryForUser(actual, other); err == nil {
		t.Fatal("foreign owner was accepted")
	}
}
