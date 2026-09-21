package lifecycle

import (
	"strings"
	"testing"
	"time"
)

func digest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func releaseSpec(version string, releasedAt time.Time, stateSchema uint32) GenerationSpec {
	return GenerationSpec{
		Version:          version,
		ReleasedAt:       releasedAt,
		HostBinaryDigest: digest("a"),
		CoreImageDigest:  digest("b"),
		Components: []Component{
			{Name: "signing", Digest: digest("d"), Optional: true},
			{Name: "browser", Digest: digest("c"), Optional: true},
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

func generationFixture(t *testing.T, version string, releasedAt time.Time, stateSchema uint32) Generation {
	t.Helper()
	generation, err := NewGeneration(releaseSpec(version, releasedAt, stateSchema))
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func hostFixture(active Generation, stateSchema uint32) HostState {
	return HostState{
		ActiveGenerationID: active.ID,
		ConfigSchema:       1,
		PolicySchema:       2,
		ToolchainSchema:    3,
		StateSchema:        stateSchema,
		EnabledComponents:  []string{"browser"},
		Revision:           "host-revision-7",
	}
}

func TestGenerationIdentityIsCanonicalAndImmutable(t *testing.T) {
	releasedAt := time.Date(2026, 9, 21, 1, 2, 3, 456789000, time.FixedZone("fixture", 9*60*60))
	spec := releaseSpec("1.2.3", releasedAt, 2)
	spec.Components[0], spec.Components[1] = spec.Components[1], spec.Components[0]
	spec.Rollback.OptionalComponentState = []string{"signing", "browser", "browser"}
	spec.Migrations = []StateTransition{
		{From: 2, To: 3, Reversible: false},
		{From: 1, To: 2, Reversible: true},
	}
	spec.StateSchema = 3
	spec.Reads.State = SchemaRange{Min: 1, Max: 3}

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
	tampered.Spec.CoreImageDigest = digest("e")
	if tampered.Valid() {
		t.Fatal("tampered generation remained valid")
	}
}

func TestGenerationRejectsInvalidOrAmbiguousContracts(t *testing.T) {
	now := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	for name, mutate := range map[string]func(*GenerationSpec){
		"bad-digest": func(spec *GenerationSpec) {
			spec.CoreImageDigest = "latest"
		},
		"self-incompatible": func(spec *GenerationSpec) {
			spec.Reads.Config = SchemaRange{Min: 1, Max: 1}
		},
		"duplicate-component": func(spec *GenerationSpec) {
			spec.Components = append(spec.Components, spec.Components[0])
		},
		"duplicate-transition": func(spec *GenerationSpec) {
			spec.StateSchema = 2
			spec.Migrations = []StateTransition{
				{From: 1, To: 2},
				{From: 1, To: 2},
			}
		},
		"unknown-rollback-component": func(spec *GenerationSpec) {
			spec.Rollback.OptionalComponentState = []string{"unknown"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := releaseSpec("1.2.3", now, 2)
			mutate(&spec)
			if _, err := NewGeneration(spec); err == nil {
				t.Fatalf("%s contract was accepted", name)
			}
		})
	}
}

func TestPrepareComputesMigrationRollbackAndImpactWithoutMutation(t *testing.T) {
	now := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	candidateSpec := releaseSpec("2.0.0", now.Add(-time.Hour), 2)
	candidateSpec.Migrations = []StateTransition{{From: 1, To: 2, Reversible: false}}
	candidate := mustGeneration(t, candidateSpec)
	host := hostFixture(active, 1)

	plan, err := Prepare(&active, candidate, host, now)
	if err != nil {
		t.Fatal(err)
	}
	if !digestPattern.MatchString(plan.ID) || plan.ActiveGenerationID != active.ID ||
		plan.CandidateGenerationID != candidate.ID || plan.ObservedHostRevision != host.Revision {
		t.Fatalf("prepared plan identity = %#v", plan)
	}
	if !plan.Impact.RestartRequired || !plan.Impact.MigrationRequired ||
		len(plan.Impact.Migration) != 1 || plan.Impact.Migration[0].From != 1 || plan.Impact.Migration[0].To != 2 {
		t.Fatalf("migration impact = %#v", plan.Impact)
	}
	if !plan.Impact.RollbackCompatible || !plan.Impact.RollbackUsesSnapshot {
		t.Fatalf("rollback impact = %#v", plan.Impact)
	}
	if len(plan.Impact.OptionalComponents) != 1 || plan.Impact.OptionalComponents[0] != "browser" {
		t.Fatalf("optional component impact = %#v", plan.Impact.OptionalComponents)
	}
	if host.StateSchema != 1 || active.Spec.StateSchema != 1 || candidate.Spec.StateSchema != 2 {
		t.Fatal("Prepare mutated input state")
	}

	repeated, err := Prepare(&active, candidate, host, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ID != plan.ID {
		t.Fatalf("same observed state produced different plan IDs: %s != %s", repeated.ID, plan.ID)
	}
	if repeated.PreparedAt == plan.PreparedAt {
		t.Fatal("prepared timestamps unexpectedly identical")
	}
}

func TestPrepareAcceptsReversibleMigrationWithoutSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	spec := releaseSpec("2.0.0", now.Add(-time.Hour), 2)
	spec.Migrations = []StateTransition{{From: 1, To: 2, Reversible: true}}
	spec.Rollback.StateSnapshot = false
	spec.Rollback.ConfigSnapshot = false
	candidate := mustGeneration(t, spec)
	host := hostFixture(active, 1)

	plan, err := Prepare(&active, candidate, host, now)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Impact.RollbackCompatible || plan.Impact.RollbackUsesSnapshot {
		t.Fatalf("reversible rollback impact = %#v", plan.Impact)
	}
}

func TestPrepareFailsClosedOnCompatibilityRollbackAndComponentGaps(t *testing.T) {
	now := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	host := hostFixture(active, 1)

	t.Run("config", func(t *testing.T) {
		spec := releaseSpec("2.0.0", now.Add(-time.Hour), 2)
		spec.Reads.Config = SchemaRange{Min: 2, Max: 2}
		candidate := mustGeneration(t, spec)
		if _, err := Prepare(&active, candidate, host, now); err == nil || !strings.Contains(err.Error(), "config schema") {
			t.Fatalf("config incompatibility error = %v", err)
		}
	})

	t.Run("missing-migration", func(t *testing.T) {
		candidate := generationFixture(t, "2.0.0", now.Add(-time.Hour), 2)
		if _, err := Prepare(&active, candidate, host, now); err == nil || !strings.Contains(err.Error(), "migration path") {
			t.Fatalf("missing migration error = %v", err)
		}
	})

	t.Run("unsafe-rollback", func(t *testing.T) {
		spec := releaseSpec("2.0.0", now.Add(-time.Hour), 2)
		spec.Migrations = []StateTransition{{From: 1, To: 2, Reversible: false}}
		spec.Rollback.StateSnapshot = false
		spec.Rollback.ConfigSnapshot = false
		candidate := mustGeneration(t, spec)
		if _, err := Prepare(&active, candidate, host, now); err == nil || !strings.Contains(err.Error(), "rollback") {
			t.Fatalf("unsafe rollback error = %v", err)
		}
	})

	t.Run("optional-state", func(t *testing.T) {
		spec := releaseSpec("2.0.0", now.Add(-time.Hour), 1)
		spec.Rollback.OptionalComponentState = []string{"signing"}
		candidate := mustGeneration(t, spec)
		if _, err := Prepare(&active, candidate, host, now); err == nil || !strings.Contains(err.Error(), "optional-component") {
			t.Fatalf("optional state error = %v", err)
		}
	})
}

func TestStatusRejectsStalePreparedPlan(t *testing.T) {
	now := time.Date(2026, 9, 21, 6, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	spec := releaseSpec("1.1.0", now.Add(-time.Hour), 1)
	candidate := mustGeneration(t, spec)
	host := hostFixture(active, 1)
	plan, err := Prepare(&active, candidate, host, now)
	if err != nil {
		t.Fatal(err)
	}

	status, err := Status(&active, &candidate, &plan, host)
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || status.Prepared == nil || status.Prepared.ID != plan.ID {
		t.Fatalf("update status = %#v", status)
	}

	tampered := plan
	tampered.Impact.RestartRequired = false
	if tampered.Valid() {
		t.Fatal("tampered prepared plan remained valid")
	}
	if _, err = Status(&active, &candidate, &tampered, host); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("tampered plan status error = %v", err)
	}

	changed := host
	changed.Revision = "host-revision-8"
	if _, err = Status(&active, &candidate, &plan, changed); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale plan status error = %v", err)
	}
}

func mustGeneration(t *testing.T, spec GenerationSpec) Generation {
	t.Helper()
	generation, err := NewGeneration(spec)
	if err != nil {
		t.Fatal(err)
	}
	return generation
}
