package policy

import (
	"testing"

	controlpolicy "loki/internal/control/policy"
)

func TestResourceChangePreservesIdentityAndValidatesBeforePlan(t *testing.T) {
	c, contract := shippedInputs(t)
	r := controlpolicy.ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "example", Repository: "repo:example", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none"}
	plan, err := PrepareApplicationChange(c, contract, nil, ApplicationChange{Action: "set", Alias: r.Alias, Resource: &r})
	if err != nil || len(plan.Resources) != 1 || plan.ExpectedPolicy == plan.Policy {
		t.Fatalf("registration plan: %+v %v", plan, err)
	}
	if _, err := ValidateApplicationPlan(c, contract, nil, plan); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateApplicationPlan(c, contract, plan.Resources, plan); err == nil {
		t.Fatal("stale plan was accepted against a changed policy")
	}
	tampered := plan
	tampered.Changes = nil
	if _, err := ValidateApplicationPlan(c, contract, nil, tampered); err == nil {
		t.Fatal("misleading change summary was accepted")
	}
	changed := r
	changed.ID = "e9ce977e-b3fe-489e-a74f-311911859fd6"
	if _, err := PrepareApplicationChange(c, contract, plan.Resources, ApplicationChange{Action: "set", Alias: r.Alias, Resource: &changed}); err == nil {
		t.Fatal("resource UUID changed through an alias")
	}
	removed, err := PrepareApplicationChange(c, contract, plan.Resources, ApplicationChange{Action: "remove", Alias: r.Alias})
	if err != nil || len(removed.Resources) != 0 || removed.ExpectedPolicy != plan.Policy {
		t.Fatalf("removal plan: %+v %v", removed, err)
	}
	if plan.Resources[0].ID != r.ID {
		t.Fatal("preparation mutated the existing catalog")
	}
}
