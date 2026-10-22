package secret

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"loki/internal/control/policy"
	"loki/internal/state"
)

func migrationPlanFixture(t *testing.T) (Controller, []MigrationProfileMapping, []policy.ApplicationResource) {
	t.Helper()
	c := managedAccessFixture(t, true)
	for _, item := range []struct{ name, value string }{{"TOKEN", "secret-canary\n'\"<&>"}, {"EMPTY", ""}, {"ENDPOINT", "https://public.example"}} {
		if _, err := c.SetSecret(t.Context(), "web", item.name, item.value, false); err != nil {
			t.Fatal(err)
		}
	}
	r := policy.ApplicationResource{
		ID: "123e4567-e89b-42d3-a456-426614174431", Alias: "web", Repository: "owner/repo",
		Principals:   []policy.ApplicationGrant{{Principal: "agent", Metadata: true, Execute: true}},
		Environments: []string{"production", "common"}, Network: "none",
		Keys: map[string]string{"TOKEN": "secret", "EMPTY": "secret", "API_URL": "variable"},
	}
	m := MigrationProfileMapping{Profile: "web", Resource: r.ID, Keys: []MigrationKeyMapping{
		{Source: "TOKEN"}, {Source: "EMPTY"}, {Source: "ENDPOINT", Name: "API_URL", Kind: "variable"},
	}}
	return c, []MigrationProfileMapping{m}, []policy.ApplicationResource{r}
}

func TestApplicationMigrationMappingExactPrivateValuesAndPublicPlan(t *testing.T) {
	c, mappings, resources := migrationPlanFixture(t)
	base := map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/workspace"}
	plan, err := c.PrepareApplicationMigration(t.Context(), mappings, resources, base)
	if err != nil || plan.Values != 3 || plan.Revision == 0 {
		t.Fatal("prepare", err)
	}
	encoded, err := json.Marshal(plan)
	if err != nil || bytes.Contains(encoded, []byte("secret-canary")) || bytes.Contains(encoded, []byte("public.example")) || bytes.Contains(encoded, []byte("PRIVATE_KEY")) || bytes.Contains(encoded, []byte("github-app")) {
		t.Fatal("plan leaked source values or included platform credentials", err)
	}
	const operation = "123e4567-e89b-42d3-a456-426614174432"
	if err := c.WithApplicationMigrationValues(t.Context(), operation, plan, base, func([]MigrationValue) error { t.Fatal("unfenced callback"); return nil }); !errors.Is(err, ErrApplicationMigration) {
		t.Fatal("read without source fence", err)
	}
	if err := c.BeginApplicationMigration(t.Context(), plan.Revision, operation); err != nil {
		t.Fatal(err)
	}
	var borrowed [][]byte
	if err := c.WithApplicationMigrationValues(t.Context(), operation, plan, base, func(values []MigrationValue) error {
		if len(values) != 3 {
			t.Fatal("lost a mapped value")
		}
		for _, v := range values {
			borrowed = append(borrowed, v.Bytes())
			if _, err := json.Marshal(v); err == nil || fmt.Sprintf("%+v", v) != "[REDACTED]" {
				t.Fatal("private value can be serialized or formatted")
			}
			want := map[string]string{"TOKEN": "secret-canary\n'\"<&>", "EMPTY": "", "API_URL": "https://public.example"}
			if string(v.Bytes()) != want[v.Name] || v.Resource != resources[0].ID || v.Environment != "common" || v.Kind != resources[0].Keys[v.Name] {
				t.Fatal("logical value/mapping changed")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, body := range borrowed {
		if !bytes.Equal(body, make([]byte, len(body))) {
			t.Fatal("callback retained unzeroed plaintext bytes")
		}
	}
	plan.Values++
	if err := c.WithApplicationMigrationValues(t.Context(), operation, plan, base, func([]MigrationValue) error { t.Fatal("forged plan callback"); return nil }); err == nil {
		t.Fatal("accepted modified prepared metadata")
	}
}

func TestApplicationMigrationMappingRejectsBeforeAnyTargetWrite(t *testing.T) {
	for _, name := range []string{"unmapped", "duplicate-source", "target-collision", "implicit-public", "kind-conflict", "reserved", "invalid-name", "managed-profile", "unknown-environment", "nul-value", "oversized-value", "escaped-pattern-limit", "stale-source"} {
		t.Run(name, func(t *testing.T) {
			c, mappings, resources := migrationPlanFixture(t)
			m := &mappings[0]
			switch name {
			case "unmapped":
				m.Keys = m.Keys[1:]
			case "duplicate-source":
				m.Keys = append(m.Keys, m.Keys[0])
			case "target-collision":
				m.Keys[1].Name = "TOKEN"
			case "implicit-public":
				m.Keys[2].Kind = ""
			case "kind-conflict":
				m.Keys[0].Kind = "variable"
			case "reserved":
				m.Keys[0].Name = "LOKI_TOKEN"
				resources[0].Keys["LOKI_TOKEN"] = "secret"
			case "invalid-name":
				m.Keys[0].Name = "BAD.KEY"
				resources[0].Keys["BAD.KEY"] = "secret"
			case "managed-profile":
				m.Profile = "github-app"
				m.Keys = []MigrationKeyMapping{{Source: "PRIVATE_KEY"}}
			case "unknown-environment":
				m.Environment = "not-allowed"
			case "nul-value", "oversized-value", "escaped-pattern-limit":
				value := "nul\x00value"
				if name == "oversized-value" {
					value = strings.Repeat("x", 65537)
				} else if name == "escaped-pattern-limit" {
					value = strings.Repeat("\n", 40000)
				}
				if _, err := c.SetSecret(t.Context(), "web", "TOKEN", value, false); err != nil {
					t.Fatal(err)
				}
			case "stale-source":
				plan, err := c.PrepareApplicationMigration(t.Context(), mappings, resources, nil)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.SetSecret(t.Context(), "web", "TOKEN", "changed", false); err != nil {
					t.Fatal(err)
				}
				if err := c.BeginApplicationMigration(t.Context(), plan.Revision, "123e4567-e89b-42d3-a456-426614174433"); !errors.Is(err, state.ErrConflict) {
					t.Fatal("accepted stale source", err)
				}
				return
			}
			if _, err := c.PrepareApplicationMigration(t.Context(), mappings, resources, nil); err == nil {
				t.Fatal("accepted invalid/incomplete mapping")
			}
		})
	}
}

func TestApplicationMigrationExplicitSplitLayersAndEmptyProfile(t *testing.T) {
	c, mappings, resources := migrationPlanFixture(t)
	if _, err := c.CreateProfile(t.Context(), "empty"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateProfile(t.Context(), "overrides"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetSecret(t.Context(), "overrides", "TOKEN", "production-secret", false); err != nil {
		t.Fatal(err)
	}
	mappings = append(mappings,
		MigrationProfileMapping{Profile: "empty", Resource: resources[0].ID, Environment: "common", Keys: []MigrationKeyMapping{}},
		MigrationProfileMapping{Profile: "overrides", Resource: resources[0].ID, Environment: "production", Keys: []MigrationKeyMapping{{Source: "TOKEN"}}},
	)
	if plan, err := c.PrepareApplicationMigration(t.Context(), mappings, resources, nil); err != nil || plan.Values != 4 || len(plan.Mappings) != 3 {
		t.Fatal("lost empty profile or explicit environment override", err)
	}
}
