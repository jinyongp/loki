package tools

import "testing"

func TestDisabledModulesDoNotClaimPublicNames(t *testing.T) {
	a, b := testManifest("first"), testManifest("second")
	a.Tools, b.Tools = []string{"shared"}, []string{"shared"}
	r, err := NewRegistry([]Manifest{a, b})
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Schema: 1, Release: "0.2.0", Host: Host{Kind: "local"}, Mode: ProjectHost, Tools: []Selection{{ID: "first", Enabled: true}, {ID: "second"}}}
	resolved, err := r.ResolveConfig(linuxProject, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Ordered) != 2 || len(resolved.Bindings) != 1 || resolved.Bindings[0].Module != "first" {
		t.Fatal("disabled module claimed discovery")
	}
	c.Tools[1].Enabled = true
	if _, err := r.ResolveConfig(linuxProject, c); err == nil {
		t.Fatal("enabled collision was accepted")
	}
	if _, err := r.ResolveInstallation(linuxProject, []ID{"first", "second"}); err != nil {
		t.Fatal("acquisition claimed public names:", err)
	}
}
