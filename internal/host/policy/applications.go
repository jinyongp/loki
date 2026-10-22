package policy

import (
	"errors"
	"reflect"

	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/execution"
)

// ApplicationChange changes host grants only. Removing/disabling a resource
// does not delete its provider profile or recycle its immutable UUID.
type ApplicationChange struct {
	Action   string                             `json:"action"`
	Alias    string                             `json:"alias"`
	Resource *controlpolicy.ApplicationResource `json:"resource,omitempty"`
}

// LoadApplications reads only an administrator/service-owned bounded catalog.
// Values and private provider identities are never part of this document.
func LoadApplications(path string) ([]controlpolicy.ApplicationResource, error) {
	if path == "" {
		return nil, nil
	}
	var document struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}
	if err := daemon.ReadJSON(path, &document); err != nil {
		return nil, err
	}
	if document.Resources == nil {
		return nil, errors.New("application catalog requires an explicit resources array")
	}
	return document.Resources, nil
}

func CompileApplicationFile(c config.Config, contract execution.Contract, path string) (controlpolicy.Generation, []controlpolicy.ApplicationResource, error) {
	resources, err := LoadApplications(path)
	if err != nil {
		return controlpolicy.Generation{}, nil, err
	}
	resources, err = controlpolicy.CanonicalApplications(resources, contract.Environment)
	if err != nil {
		return controlpolicy.Generation{}, nil, err
	}
	generation, err := CompileApplications(c, contract, resources)
	return generation, resources, err
}

type ApplicationPlan struct {
	Version        int                                 `json:"version"`
	ExpectedPolicy string                              `json:"expected_policy"`
	Policy         string                              `json:"policy"`
	Resources      []controlpolicy.ApplicationResource `json:"resources"`
	Changes        []controlpolicy.Change              `json:"changes"`
}

// ValidateApplicationPlan rederives both policy generations under the actual
// deployment configuration. A saved plan is public intent, not an authority to
// bypass stale revisions, reserved environment names or immutable resource IDs.
func ValidateApplicationPlan(c config.Config, contract execution.Contract, current []controlpolicy.ApplicationResource, plan ApplicationPlan) ([]controlpolicy.ApplicationResource, error) {
	if plan.Version != 1 || plan.Resources == nil {
		return nil, errors.New("invalid application policy plan")
	}
	before, err := CompileApplications(c, contract, current)
	if err != nil || before.Digest() != plan.ExpectedPolicy {
		return nil, errors.New("application policy plan is stale")
	}
	next, err := controlpolicy.CanonicalApplications(plan.Resources, contract.Environment)
	if err != nil {
		return nil, err
	}
	for _, old := range current {
		for _, proposed := range next {
			if old.Alias == proposed.Alias && old.ID != proposed.ID {
				return nil, errors.New("existing resource UUID is immutable")
			}
		}
	}
	after, err := CompileApplications(c, contract, next)
	if err != nil || after.Digest() != plan.Policy {
		return nil, errors.New("application policy plan does not match deployment inputs")
	}
	diff, err := controlpolicy.Diff(before, after)
	if err != nil || !reflect.DeepEqual(diff, plan.Changes) {
		return nil, errors.New("application policy change summary does not match its resources")
	}
	return next, nil
}

func PrepareApplicationChange(c config.Config, contract execution.Contract, current []controlpolicy.ApplicationResource, change ApplicationChange) (ApplicationPlan, error) {
	before, err := CompileApplications(c, contract, current)
	if err != nil {
		return ApplicationPlan{}, err
	}
	if !controlpolicy.ApplicationAlias.MatchString(change.Alias) {
		return ApplicationPlan{}, errors.New("invalid resource alias")
	}
	if change.Action == "set" {
		if change.Resource == nil || change.Resource.Alias != change.Alias {
			return ApplicationPlan{}, errors.New("resource replacement requires matching alias")
		}
	} else if (change.Action != "remove" && change.Action != "disable") || change.Resource != nil {
		return ApplicationPlan{}, errors.New("invalid resource change")
	}
	next := make([]controlpolicy.ApplicationResource, 0, len(current)+1)
	found := false
	for _, r := range current {
		if r.Alias != change.Alias {
			next = append(next, r)
			continue
		}
		found = true
		if change.Resource != nil && r.ID != change.Resource.ID {
			return ApplicationPlan{}, errors.New("existing resource UUID is immutable")
		}
	}
	if change.Resource != nil {
		next = append(next, *change.Resource)
	} else if !found {
		return ApplicationPlan{}, errors.New("resource does not exist")
	}
	next, err = controlpolicy.CanonicalApplications(next, contract.Environment)
	if err != nil {
		return ApplicationPlan{}, err
	}
	after, err := CompileApplications(c, contract, next)
	if err != nil {
		return ApplicationPlan{}, err
	}
	diff, err := controlpolicy.Diff(before, after)
	if err != nil {
		return ApplicationPlan{}, err
	}
	return ApplicationPlan{Version: 1, ExpectedPolicy: before.Digest(), Policy: after.Digest(), Resources: next, Changes: diff}, nil
}
