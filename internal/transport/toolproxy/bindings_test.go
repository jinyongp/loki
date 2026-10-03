package toolproxy

import "testing"

func TestDiscoveryCollisionKeepsExistingOwners(t *testing.T) {
	owners := &bindingOwners{}
	if err := owners.replace("browser/playwright", []string{"browser_navigate"}, func() {}); err != nil {
		t.Fatal(err)
	}
	if err := owners.replace("browser/devtools", []string{"navigate_page"}, func() {}); err != nil {
		t.Fatal(err)
	}
	applied := false
	if err := owners.replace("browser/devtools", []string{"browser_navigate"}, func() { applied = true }); err == nil {
		t.Fatal("overwrote another engine")
	}
	devtools, _ := owners.registry.Owner("tools", "navigate_page")
	playwright, _ := owners.registry.Owner("tools", "browser_navigate")
	if applied || devtools != "browser/devtools" || playwright != "browser/playwright" {
		t.Fatal("failed refresh changed ownership")
	}
	if err := owners.replace("browser/playwright", nil, func() {}); err != nil {
		t.Fatal(err)
	}
	if err := owners.replace("browser/devtools", []string{"browser_navigate"}, func() {}); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryDuplicateNamesDoNotApply(t *testing.T) {
	owners := &bindingOwners{}
	if err := owners.replace("engine", []string{"same", "same"}, func() { t.Fatal("invalid discovery applied") }); err == nil {
		t.Fatal("accepted duplicate discovery")
	}
}
