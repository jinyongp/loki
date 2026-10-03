package tools

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

var linuxProject = Target{OS: "linux", Arch: "amd64", Mode: ProjectHost}

func testManifest(id ID, requires ...ID) Manifest {
	return Manifest{Schema: ManifestSchema, ID: id, Release: "0.2.0", Targets: []Target{linuxProject}, Requires: requires, Tools: []string{string(id) + ".inspect"}}
}

func TestManifestInputContract(t *testing.T) {
	valid := testManifest("browser")
	data, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(data); err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string][]byte{
		"unknown field":     []byte(strings.TrimSuffix(string(data), "}") + `,"credentials":"unexpected"}`),
		"trailing document": append(append([]byte(nil), data...), []byte(` {}`)...),
		"trailing garbage":  append(append([]byte(nil), data...), 'x'),
		"oversize":          []byte(strings.Repeat(" ", MaxManifestBytes+1)),
		"null":              []byte("null"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest(input); err == nil {
				t.Fatal("invalid document accepted")
			}
		})
	}
	for name, mutate := range map[string]func(*Manifest){
		"schema":                 func(m *Manifest) { m.Schema++ },
		"path ID":                func(m *Manifest) { m.ID = "../browser" },
		"release":                func(m *Manifest) { m.Release = "0.1.52" },
		"noncanonical release":   func(m *Manifest) { m.Release = "0.2.01" },
		"missing target":         func(m *Manifest) { m.Targets = nil },
		"unknown OS":             func(m *Manifest) { m.Targets[0].OS = "plan9" },
		"unknown architecture":   func(m *Manifest) { m.Targets[0].Arch = "386" },
		"unknown mode":           func(m *Manifest) { m.Targets[0].Mode = "automatic" },
		"duplicate target":       func(m *Manifest) { m.Targets = append(m.Targets, m.Targets[0]) },
		"self prerequisite":      func(m *Manifest) { m.Requires = []ID{m.ID} },
		"duplicate prerequisite": func(m *Manifest) { m.Requires = []ID{"execution", "execution"} },
		"duplicate binding":      func(m *Manifest) { m.Tools = append(m.Tools, m.Tools[0]) },
		"invalid binding":        func(m *Manifest) { m.Tools = []string{"../browser"} },
	} {
		t.Run(name, func(t *testing.T) {
			manifest := cloneManifest(valid)
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestRegistryRejectsInvalidGraph(t *testing.T) {
	for name, manifests := range map[string][]Manifest{
		"duplicate ID":         {testManifest("browser"), testManifest("browser")},
		"unknown prerequisite": {testManifest("git", "execution")},
		"cycle":                {testManifest("git", "execution"), testManifest("execution", "git")},
		"unselected cycle":     {testManifest("browser"), testManifest("git", "execution"), testManifest("execution", "git")},
		"mixed release":        {testManifest("browser"), {Schema: ManifestSchema, ID: "git", Release: "0.2.1", Targets: []Target{linuxProject}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRegistry(manifests); err == nil {
				t.Fatal("invalid catalog accepted")
			}
		})
	}
}

func TestSelectionSeparatesPrivatePrerequisites(t *testing.T) {
	registry, err := NewRegistry([]Manifest{testManifest("git", "execution"), testManifest("execution"), testManifest("browser")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		selected []ID
		ordered  []ID
		public   []ID
	}{
		{selected: nil},
		{selected: []ID{"browser"}, ordered: []ID{"browser"}, public: []ID{"browser"}},
		{selected: []ID{"git"}, ordered: []ID{"execution", "git"}, public: []ID{"git"}},
		{selected: []ID{"git", "execution", "browser"}, ordered: []ID{"browser", "execution", "git"}, public: []ID{"browser", "execution", "git"}},
	} {
		resolution, err := registry.Resolve(linuxProject, tc.selected)
		if err != nil {
			t.Fatal(err)
		}
		var ordered, public []ID
		for _, manifest := range resolution.Ordered {
			ordered = append(ordered, manifest.ID)
		}
		for _, binding := range resolution.Bindings {
			public = append(public, binding.Module)
		}
		if !slices.Equal(ordered, tc.ordered) || !slices.Equal(public, tc.public) {
			t.Fatalf("selection %v: ordered=%v public=%v", tc.selected, ordered, public)
		}
	}
	for _, selected := range [][]ID{{"missing"}, {"git", "git"}} {
		if _, err := registry.Resolve(linuxProject, selected); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	for _, target := range []Target{{OS: "darwin", Arch: "arm64", Mode: ProjectHost}, {OS: "linux", Arch: "amd64", Mode: Full}} {
		if _, err := registry.Resolve(target, []ID{"browser"}); err == nil {
			t.Fatal("unsupported target accepted")
		}
	}
}

func TestResolverRejectsUnsupportedPrivateDependencyAndBindingCollision(t *testing.T) {
	execution := testManifest("execution")
	execution.Targets = []Target{{OS: "linux", Arch: "amd64", Mode: Full}}
	registry, err := NewRegistry([]Manifest{testManifest("git", "execution"), execution})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(linuxProject, []ID{"git"}); err == nil {
		t.Fatal("unsupported private prerequisite accepted")
	}
	a, b := testManifest("browser"), testManifest("git")
	b.Tools = a.Tools
	registry, err = NewRegistry([]Manifest{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(linuxProject, []ID{"browser"}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Resolve(linuxProject, []ID{"browser", "git"}); err == nil {
		t.Fatal("binding collision accepted")
	}
}

func TestRegistryOwnsItsInputAndResults(t *testing.T) {
	manifest := testManifest("browser")
	registry, err := NewRegistry([]Manifest{manifest})
	if err != nil {
		t.Fatal(err)
	}
	manifest.Tools[0] = "changed"
	manifest.Targets[0].OS = "windows"
	resolution, err := registry.Resolve(linuxProject, []ID{"browser"})
	if err != nil {
		t.Fatal(err)
	}
	resolution.Ordered[0].Tools[0] = "also_changed"
	resolution.Ordered[0].Targets[0].OS = "darwin"
	again, err := registry.Resolve(linuxProject, []ID{"browser"})
	if err != nil || again.Bindings[0].Name != "browser.inspect" || again.Ordered[0].Tools[0] != "browser.inspect" {
		t.Fatalf("registry changed through an external slice: %+v %v", again, err)
	}
}
