package policy

import (
	"errors"

	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/execution"
)

// ApplicationChange changes host grants only. Removing/disabling a resource
// does not delete its provider profile or recycle its immutable UUID.
type ApplicationChange struct {
	Action   string                             `json:"action"`
	Alias    string                             `json:"alias"`
	Resource *controlpolicy.ApplicationResource `json:"resource,omitempty"`
}

type ApplicationPlan struct {
	Version        int                                 `json:"version"`
	ExpectedPolicy string                              `json:"expected_policy"`
	Policy         string                              `json:"policy"`
	Resources      []controlpolicy.ApplicationResource `json:"resources"`
	Changes        []controlpolicy.Change              `json:"changes"`
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
