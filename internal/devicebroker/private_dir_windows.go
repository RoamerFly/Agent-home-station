//go:build windows

package devicebroker

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ensurePrivateDirectory creates only the final shared lease leaf with an
// explicit protected DACL. Existing directories are never taken over: their
// owner must already be the current token user before their DACL is repaired.
func ensurePrivateDirectory(path string) (string, error) {
	user, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return ensurePrivateDirectoryForUser(path, user)
}

func ensurePrivateDirectoryForUser(path string, user *windows.SID) (string, error) {
	descriptor, err := privateDescriptor(user)
	if err != nil {
		return "", err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	if err = windows.CreateDirectory(name, &attributes); err != nil {
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return "", err
		}
		if err = verifyCurrentOwner(path, user); err != nil {
			return "", err
		}
		if err = applyPrivateDACL(path, user, descriptor); err != nil {
			return "", err
		}
	}
	canonical, err := canonicalDirectory(path)
	if err != nil {
		return "", err
	}
	if err = verifyPrivateDirectory(canonical); err != nil {
		return "", err
	}
	return canonical, nil
}

func currentUserSID() (*windows.SID, error) {
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid.Copy()
}

func privateDescriptor(user *windows.SID) (*windows.SECURITY_DESCRIPTOR, error) {
	// O: pins ownership to the current user. D:P protects the DACL from parent
	// inheritance; only the owner and LocalSystem receive full access, inherited
	// by lease files created under this directory.
	return windows.SecurityDescriptorFromString("O:" + user.String() + "D:P(A;OICI;FA;;;" + user.String() + ")(A;OICI;FA;;;SY)")
}

func verifyCurrentOwner(path string, user *windows.SID) error {
	descriptor, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		return errors.New("private directory owner mismatch")
	}
	return nil
}

func applyPrivateDACL(path string, user *windows.SID, descriptor *windows.SECURITY_DESCRIPTOR) error {
	dacl, _, err := descriptor.DACL()
	if err != nil || dacl == nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, user, nil, dacl, nil)
}

func verifyPrivateDirectory(path string) error {
	canonical, err := canonicalDirectory(path)
	if err != nil || !samePath(canonical, path) {
		return errors.New("private directory changed")
	}
	user, err := currentUserSID()
	if err != nil {
		return err
	}
	descriptor, err := windows.GetNamedSecurityInfo(canonical, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || !owner.Equals(user) {
		return errors.New("private directory owner mismatch")
	}
	sddl := descriptor.String()
	if !strings.Contains(sddl, "D:P") || !strings.Contains(sddl, ";;;"+user.String()+")") || !strings.Contains(sddl, ";;;SY)") || strings.Count(sddl, "(A;") != 2 {
		return errors.New("private directory dacl mismatch")
	}
	return nil
}
