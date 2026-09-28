//go:build windows

package windows

import (
	"context"
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func applyPrivateACL(ctx context.Context, target string, directory bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	userInfo, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if userInfo == nil || userInfo.User.Sid == nil {
		return errors.New("current Windows process token has no user SID")
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}

	var pinner runtime.Pinner
	defer pinner.Unpin()
	pinner.Pin(userInfo)
	pinner.Pin(userInfo.User.Sid)
	pinner.Pin(systemSID)

	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	access := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(userInfo.User.Sid),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inheritance,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(systemSID),
			},
		},
	}
	acl, err := windows.ACLFromEntries(access, nil)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	err = windows.SetNamedSecurityInfo(
		target,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
	runtime.KeepAlive(userInfo)
	runtime.KeepAlive(systemSID)
	runtime.KeepAlive(acl)
	return err
}

func verifyPrivateACL(target string, directory bool) error {
	sd, err := windows.GetNamedSecurityInfo(
		target,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return err
	}
	if sd == nil {
		return errors.New("Windows path has no security descriptor")
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("Windows path DACL is not protected from inheritance")
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil || dacl.AceCount == 0 {
		return errors.New("Windows path DACL is empty")
	}
	userInfo, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if userInfo == nil || userInfo.User.Sid == nil {
		return errors.New("current Windows process token has no user SID")
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	expectedFlags := uint8(windows.NO_INHERITANCE)
	if directory {
		expectedFlags = uint8(windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	}
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1FF
	const allowedAceFlags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE | windows.INHERITED_ACE
	foundUser := false
	foundSystem := false
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("Windows path DACL contains a non-allow access rule")
		}
		flags := ace.Header.AceFlags
		if flags&^uint8(allowedAceFlags) != 0 ||
			flags&uint8(windows.OBJECT_INHERIT_ACE|windows.CONTAINER_INHERIT_ACE) != expectedFlags {
			return errors.New("Windows path DACL contains unexpected inheritance flags")
		}
		if ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&fileAllAccess != fileAllAccess {
			return errors.New("Windows path DACL contains a rule without full control")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		switch {
		case sid.Equals(userInfo.User.Sid):
			foundUser = true
		case sid.Equals(systemSID):
			foundSystem = true
		default:
			return errors.New("Windows path DACL grants access to an unexpected principal")
		}
	}
	if !foundUser || !foundSystem {
		return errors.New("Windows path DACL is missing the current user or LocalSystem")
	}
	return nil
}
