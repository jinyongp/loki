package policy

import "testing"

func applicationFixture() ApplicationResource {
	return ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "example", Repository: "repo:example", Principals: []ApplicationGrant{{Principal: "agent", Metadata: true, Write: true, Execute: true}}, Environments: []string{"common", "production"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none"}
}

func TestApplicationPolicyRejectsAuthorityExpansion(t *testing.T) {
	for _, name := range []string{"PATH", "path", "HTTPS_PROXY", "https_proxy", "XDG_CONFIG_HOME", "LD_PRELOAD", "GIT_CONFIG_COUNT", "DOCKER_HOST", "LOKI_RUNTIME_SOCKET", "BASH_ENV", "NODE_OPTIONS", "CONTRACT_ADDITION"} {
		r := applicationFixture()
		r.Keys = map[string]string{name: "secret"}
		if _, err := CanonicalApplications([]ApplicationResource{r}, map[string]string{"CONTRACT_ADDITION": "fixed"}); err == nil {
			t.Fatalf("reserved key admitted: %s", name)
		}
	}
	for _, mutate := range []func(*ApplicationResource){
		func(r *ApplicationResource) { r.ID = "9AF6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2" },
		func(r *ApplicationResource) { r.Keys = map[string]string{"BAD.NAME": "secret"} },
		func(r *ApplicationResource) { r.Keys["OTHER"] = "unknown" },
		func(r *ApplicationResource) { r.Environments = []string{"common", "common"} },
		func(r *ApplicationResource) { r.Principals = nil },
		func(r *ApplicationResource) { r.Network = "dependency-install" },
		func(r *ApplicationResource) { r.Destinations = []string{"example.com:443"} },
		func(r *ApplicationResource) { r.Network = "application"; r.Destinations = []string{"127.0.0.1:443"} },
		func(r *ApplicationResource) {
			r.Network = "application"
			r.Destinations = []string{"*.example.com:443"}
		},
	} {
		r := applicationFixture()
		mutate(&r)
		if _, err := CanonicalApplications([]ApplicationResource{r}, nil); err == nil {
			t.Fatal("invalid grant admitted")
		}
	}
}

func TestApplicationPolicyBindsPrincipalRepositoryAndEnvironment(t *testing.T) {
	r := applicationFixture()
	if err := r.Authorize("agent", "repo:example", "", "execute"); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][4]string{{"other", "repo:example", "common", "execute"}, {"agent", "repo:other", "common", "execute"}, {"agent", "repo:example", "staging", "execute"}, {"agent", "repo:example", "common", "admin"}} {
		if err := r.Authorize(args[0], args[1], args[2], args[3]); err == nil {
			t.Fatalf("unauthorized: %v", args)
		}
	}
	got, err := CanonicalApplications([]ApplicationResource{r}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Keys["PATH"] = "secret"
	r.Environments[0] = "other"
	r.Principals[0].Principal = "other"
	if len(got[0].Keys) != 1 || got[0].Environments[0] != "common" || got[0].Principals[0].Principal != "agent" {
		t.Fatal("published grant aliases mutable input")
	}
}
