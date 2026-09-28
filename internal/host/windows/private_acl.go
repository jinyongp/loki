package windows

import "errors"

// Win32 ACE constants are kept here so the security policy can be tested on
// every host. private_acl_windows.go is the only native descriptor decoder.
const (
	privateACEAllow       = uint8(0)
	privateACEObject      = uint8(0x01)
	privateACEContainer   = uint8(0x02)
	privateACEInheritOnly = uint8(0x08)
	privateACEInherited   = uint8(0x10)
	privateGenericAll     = uint32(0x10000000)
	privateFileAll        = uint32(0x001f01ff)
)

type privateACLPrincipal uint8

const (
	privateACLForeign privateACLPrincipal = iota
	privateACLCurrentUser
	privateACLSystem
)

type privateACLRule struct {
	Principal privateACLPrincipal
	Type      uint8
	Flags     uint8
	Mask      uint32
}

func validatePrivateACLRules(rules []privateACLRule, directory bool) error {
	if len(rules) == 0 {
		return errors.New("Windows path DACL is empty")
	}
	const (
		effective = uint8(1 << iota)
		childFile
		childDirectory
	)
	var coverage [3]uint8
	for _, rule := range rules {
		if rule.Type != privateACEAllow {
			return errors.New("Windows path DACL contains a non-allow access rule")
		}
		if rule.Principal != privateACLCurrentUser && rule.Principal != privateACLSystem {
			return errors.New("Windows path DACL grants access to an unexpected principal")
		}
		if rule.Mask & ^(privateGenericAll|privateFileAll) != 0 ||
			(rule.Mask&privateGenericAll == 0 && rule.Mask&privateFileAll != privateFileAll) {
			return errors.New("Windows path DACL contains a rule without exact file full control")
		}
		flags := rule.Flags &^ privateACEInherited
		if !directory && flags != 0 {
			return errors.New("Windows file DACL contains inheritance flags")
		}
		if flags & ^(privateACEObject|privateACEContainer|privateACEInheritOnly) != 0 {
			return errors.New("Windows path DACL contains unexpected inheritance flags")
		}
		if flags&privateACEInheritOnly != 0 && flags&(privateACEObject|privateACEContainer) == 0 {
			return errors.New("Windows path DACL contains an ineffective inherit-only rule")
		}
		// Windows can split a generic inheritable ACE into an effective-only
		// specific-rights ACE and an inherit-only generic-rights ACE. Neither
		// alone proves access to both this object and its descendants.
		// https://learn.microsoft.com/en-us/windows/win32/secauthz/ace-inheritance-rules
		if flags&privateACEInheritOnly == 0 {
			coverage[rule.Principal] |= effective
		}
		if flags&privateACEObject != 0 {
			coverage[rule.Principal] |= childFile
		}
		if flags&privateACEContainer != 0 {
			coverage[rule.Principal] |= childDirectory
		}
	}
	want := effective
	if directory {
		want |= childFile | childDirectory
	}
	if coverage[privateACLCurrentUser] != want || coverage[privateACLSystem] != want {
		return errors.New("Windows path DACL lacks current-user or LocalSystem full control in a required scope")
	}
	return nil
}
