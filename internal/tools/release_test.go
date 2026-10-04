package tools

import "testing"

func TestExplicitCompositionAllowsIndependentArtifactVersions(t *testing.T) {
	a, b := testManifest("git", "execution"), testManifest("execution")
	a.Contract, b.Contract = CompositionContract, CompositionContract
	a.Release, b.Release = "0.2.4", "0.2.5"
	r, err := NewRegistry([]Manifest{a, b})
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Schema: 1, Contract: CompositionContract, Release: "0.2.6", Host: Host{Kind: "local"}, Mode: ProjectHost, Tools: []Selection{{ID: "git", Enabled: true}}}
	resolved, err := r.ResolveConfig(linuxProject, c)
	if err != nil || len(resolved.Ordered) != 2 || len(resolved.Bindings) != 1 {
		t.Fatalf("mixed composition failed: %+v %v", resolved, err)
	}
	c.Contract = ""
	if _, err := r.ResolveConfig(linuxProject, c); err == nil {
		t.Fatal("explicit manifests entered a legacy configuration")
	}
	b.Contract = ""
	if _, err := NewRegistry([]Manifest{a, b}); err == nil {
		t.Fatal("mixed compatibility contracts accepted")
	}
}
