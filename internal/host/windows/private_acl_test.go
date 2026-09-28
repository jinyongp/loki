package windows

import "testing"

func privateACLFixture(directory bool) []privateACLRule {
	flags := uint8(0)
	if directory {
		flags = privateACEObject | privateACEContainer
	}
	return []privateACLRule{
		{Principal: privateACLCurrentUser, Mask: privateFileAll, Flags: flags},
		{Principal: privateACLSystem, Mask: privateFileAll, Flags: flags},
	}
}

func TestPrivateACLAcceptsWindowsSplitInheritance(t *testing.T) {
	split := []privateACLRule{}
	for _, principal := range []privateACLPrincipal{privateACLCurrentUser, privateACLSystem} {
		split = append(split,
			privateACLRule{Principal: principal, Mask: privateFileAll, Flags: privateACEInherited},
			privateACLRule{Principal: principal, Mask: privateGenericAll,
				Flags: privateACEObject | privateACEContainer | privateACEInheritOnly | privateACEInherited},
		)
	}
	if err := validatePrivateACLRules(split, true); err != nil {
		t.Fatalf("Windows effective/inherit-only split rejected: %v", err)
	}
	// Windows may retain equivalent explicit ACEs as well as inherited ones.
	if err := validatePrivateACLRules(append(split, privateACLFixture(true)...), true); err != nil {
		t.Fatalf("equivalent additional ACEs rejected: %v", err)
	}
	if err := validatePrivateACLRules(privateACLFixture(false), false); err != nil {
		t.Fatalf("private file rejected: %v", err)
	}
}

func TestPrivateACLRejectsIncompleteOrBroadenedPermissions(t *testing.T) {
	for _, name := range []string{
		"foreign", "foreign-inherit-only", "deny", "partial-rights", "extra-rights",
		"missing-user", "missing-system", "inherit-only", "effective-only",
		"missing-file-inheritance", "missing-directory-inheritance", "no-propagate",
		"unknown-flags", "inert-inherit-only", "empty",
	} {
		t.Run(name, func(t *testing.T) {
			rules := privateACLFixture(true)
			switch name {
			case "foreign":
				rules = append(rules, privateACLRule{Principal: privateACLForeign, Mask: privateFileAll})
			case "foreign-inherit-only":
				rules = append(rules, privateACLRule{Principal: privateACLForeign, Mask: privateGenericAll,
					Flags: privateACEObject | privateACEContainer | privateACEInheritOnly})
			case "deny":
				rules[0].Type = 1
			case "partial-rights":
				rules[0].Mask = 0x00120089
			case "extra-rights":
				rules[0].Mask |= 0x01000000
			case "missing-user":
				rules = rules[1:]
			case "missing-system":
				rules = rules[:1]
			case "inherit-only":
				rules[0].Flags |= privateACEInheritOnly
			case "effective-only":
				rules[0].Flags = 0
			case "missing-file-inheritance":
				rules[0].Flags = privateACEContainer
			case "missing-directory-inheritance":
				rules[0].Flags = privateACEObject
			case "no-propagate":
				rules[0].Flags |= 0x04
			case "unknown-flags":
				rules[0].Flags |= 0x80
			case "inert-inherit-only":
				rules = append(rules, privateACLRule{Principal: privateACLCurrentUser,
					Mask: privateGenericAll, Flags: privateACEInheritOnly})
			case "empty":
				rules = nil
			}
			if err := validatePrivateACLRules(rules, true); err == nil {
				t.Fatal("unsafe or incomplete ACL accepted")
			}
		})
	}
}

func TestPrivateACLFileRequiresEffectiveRules(t *testing.T) {
	for _, flags := range []uint8{privateACEObject, privateACEContainer, privateACEInheritOnly,
		privateACEObject | privateACEContainer | privateACEInheritOnly} {
		rules := privateACLFixture(false)
		rules[0].Flags = flags
		if err := validatePrivateACLRules(rules, false); err == nil {
			t.Fatalf("file accepted ineffective/inheritable flags %#x", flags)
		}
	}
	rules := privateACLFixture(false)
	for index := range rules {
		rules[index].Flags = privateACEInherited
	}
	if err := validatePrivateACLRules(rules, false); err != nil {
		t.Fatalf("effective inherited file ACE rejected: %v", err)
	}
}
