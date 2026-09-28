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
	rules := make([]privateACLRule, 0, dacl.AceCount)
	for index := uint32(0); index < uint32(dacl.AceCount); index++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(dacl, index, &ace); err != nil {
			return err
		}
		if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return errors.New("Windows path DACL contains a non-allow access rule")
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		principal := privateACLForeign
		switch {
		case sid.Equals(userInfo.User.Sid):
			principal = privateACLCurrentUser
		case sid.Equals(systemSID):
			principal = privateACLSystem
		}
		rules = append(rules, privateACLRule{
			Principal: principal, Type: ace.Header.AceType,
			Flags: ace.Header.AceFlags, Mask: uint32(ace.Mask),
		})
	}
	err = validatePrivateACLRules(rules, directory)
	runtime.KeepAlive(sd)
	runtime.KeepAlive(userInfo)
	runtime.KeepAlive(systemSID)
	return err
}
