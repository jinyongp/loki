package tools

import "testing"

func TestDiscoveryRequiresSelectedEnabledReadyResources(t *testing.T) {
	registry, err := NewRegistry([]Manifest{testManifest("git", "execution"), testManifest("execution")})
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := registry.Resolve(linuxProject, []ID{"git"})
	if err != nil {
		t.Fatal(err)
	}
	ready := State{Installed: true, Release: "0.2.0", Enabled: true, Readiness: Ready}
	private := ready
	private.Enabled = false
	for _, tc := range []struct {
		name       string
		git        State
		dependency *State
		want       int
	}{
		{name: "ready private service", git: ready, dependency: &private, want: 1},
		{name: "public group disabled", git: State{Installed: true, Release: "0.2.0", Readiness: Ready}, dependency: &private},
		{name: "unknown readiness", git: State{Installed: true, Release: "0.2.0", Enabled: true, Readiness: Unknown}, dependency: &private},
		{name: "degraded", git: State{Installed: true, Release: "0.2.0", Enabled: true, Readiness: Degraded}, dependency: &private},
		{name: "missing dependency", git: ready},
		{name: "dependency wrong release", git: ready, dependency: &State{Installed: true, Release: "0.2.1", Readiness: Ready}},
		{name: "dependency degraded", git: ready, dependency: &State{Installed: true, Release: "0.2.0", Readiness: Degraded}},
		{name: "selected wrong release", git: State{Installed: true, Release: "0.2.1", Enabled: true, Readiness: Ready}, dependency: &private},
		{name: "absent", git: State{Readiness: Unknown}, dependency: &private},
	} {
		t.Run(tc.name, func(t *testing.T) {
			states := map[ID]State{"git": tc.git}
			if tc.dependency != nil {
				states["execution"] = *tc.dependency
			}
			bindings, err := resolution.AvailableBindings(states)
			if err != nil || len(bindings) != tc.want {
				t.Fatalf("bindings=%v error=%v", bindings, err)
			}
			if len(bindings) > 0 && bindings[0].Module != "git" {
				t.Fatal("private prerequisite leaked")
			}
		})
	}
}

func TestStateRejectsContradictoryObservations(t *testing.T) {
	for _, state := range []State{
		{Enabled: true, Readiness: Unknown},
		{Readiness: Ready},
		{Installed: true, Readiness: Ready},
		{Installed: true, Release: "0.1.52", Readiness: Ready},
		{Installed: true, Release: "0.2.0", Readiness: "yes"},
	} {
		if err := state.Validate(); err == nil {
			t.Fatalf("contradictory state accepted: %+v", state)
		}
	}
}
