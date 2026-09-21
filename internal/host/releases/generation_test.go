package releases

import (
	"strings"
	"testing"
	"time"
)

func releaseDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func releaseGenerationSpec(version string, releasedAt time.Time, stateSchema uint32) GenerationSpec {
	return GenerationSpec{
		Version:          version,
		ReleasedAt:       releasedAt,
		HostBinaryDigest: releaseDigest("a"),
		CoreImageDigest:  releaseDigest("b"),
		Components: []Component{
			{Name: "signing", Digest: releaseDigest("d"), Optional: true},
			{Name: "browser", Digest: releaseDigest("c"), Optional: true},
		},
		ConfigSchema:    2,
		PolicySchema:    3,
		ToolchainSchema: 4,
		StateSchema:     stateSchema,
		Reads: Compatibility{
			Config:    SchemaRange{Min: 1, Max: 2},
			Policy:    SchemaRange{Min: 2, Max: 3},
			Toolchain: SchemaRange{Min: 3, Max: 4},
			State:     SchemaRange{Min: 1, Max: stateSchema},
		},
		Rollback: RollbackCoverage{
			StateSnapshot:          true,
			ConfigSnapshot:         true,
			OptionalComponentState: []string{"signing", "browser"},
		},
	}
}

func releaseGenerationFixture(t *testing.T, version string, releasedAt time.Time, stateSchema uint32) Generation {
	t.Helper()
	generation, err := NewGeneration(releaseGenerationSpec(version, releasedAt, stateSchema))
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestReleaseGenerationIdentityIsCanonicalAndImmutable(t *testing.T) {
	releasedAt := time.Date(2026, 9, 21, 1, 2, 3, 456789000, time.FixedZone("fixture", 9*60*60))
	spec := releaseGenerationSpec("1.2.3", releasedAt, 3)
	spec.Components[0], spec.Components[1] = spec.Components[1], spec.Components[0]
	spec.Rollback.OptionalComponentState = []string{"signing", "browser", "browser"}
	spec.Migrations = []StateTransition{
		{From: 2, To: 3, Reversible: false},
		{From: 1, To: 2, Reversible: true},
	}

	first, err := NewGeneration(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Valid() || first.Spec.ReleasedAt.Location() != time.UTC || first.Spec.ReleasedAt.Nanosecond() != 0 {
		t.Fatalf("normalized generation = %#v", first)
	}
	if first.Spec.Components[0].Name != "browser" || first.Spec.Components[1].Name != "signing" {
		t.Fatalf("components are not canonical: %#v", first.Spec.Components)
	}
	if len(first.Spec.Rollback.OptionalComponentState) != 2 ||
		first.Spec.Rollback.OptionalComponentState[0] != "browser" ||
		first.Spec.Rollback.OptionalComponentState[1] != "signing" {
		t.Fatalf("rollback coverage is not canonical: %#v", first.Spec.Rollback.OptionalComponentState)
	}
	if first.Spec.Migrations[0].From != 1 || first.Spec.Migrations[1].From != 2 {
		t.Fatalf("migrations are not canonical: %#v", first.Spec.Migrations)
	}

	reordered := spec
	reordered.Components = append([]Component(nil), spec.Components...)
	reordered.Components[0], reordered.Components[1] = reordered.Components[1], reordered.Components[0]
	reordered.Migrations = []StateTransition{
		{From: 1, To: 2, Reversible: true},
		{From: 2, To: 3, Reversible: false},
	}
	reordered.Rollback.OptionalComponentState = []string{"browser", "signing"}
	second, err := NewGeneration(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("canonical generation IDs differ: %s != %s", second.ID, first.ID)
	}

	tampered := first
	tampered.Spec.CoreImageDigest = releaseDigest("e")
	if tampered.Valid() {
		t.Fatal("tampered generation remained valid")
	}
}

func TestReleaseGenerationRejectsInvalidCompatibilityContracts(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	tests := map[string]func(*GenerationSpec){
		"bad-digest": func(spec *GenerationSpec) {
			spec.CoreImageDigest = "latest"
		},
		"self-incompatible-config": func(spec *GenerationSpec) {
			spec.Reads.Config = SchemaRange{Min: 1, Max: 1}
		},
		"self-incompatible-policy": func(spec *GenerationSpec) {
			spec.Reads.Policy = SchemaRange{Min: 1, Max: 2}
		},
		"self-incompatible-toolchain": func(spec *GenerationSpec) {
			spec.Reads.Toolchain = SchemaRange{Min: 1, Max: 3}
		},
		"self-incompatible-state": func(spec *GenerationSpec) {
			spec.Reads.State = SchemaRange{Min: 1, Max: 1}
		},
		"duplicate-component": func(spec *GenerationSpec) {
			spec.Components = append(spec.Components, spec.Components[0])
		},
		"duplicate-transition": func(spec *GenerationSpec) {
			spec.Migrations = []StateTransition{
				{From: 1, To: 2},
				{From: 1, To: 2},
			}
		},
		"invalid-transition": func(spec *GenerationSpec) {
			spec.Migrations = []StateTransition{{From: 2, To: 2}}
		},
		"unknown-rollback-component": func(spec *GenerationSpec) {
			spec.Rollback.OptionalComponentState = []string{"unknown"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			spec := releaseGenerationSpec("1.2.3", now, 2)
			mutate(&spec)
			if _, err := NewGeneration(spec); err == nil {
				t.Fatalf("%s contract was accepted", name)
			}
		})
	}
}
