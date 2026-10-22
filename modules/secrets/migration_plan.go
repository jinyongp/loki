package secret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"loki/internal/control/policy"
	"loki/internal/platform/privateio"
	"loki/internal/secretoutput"
	"loki/internal/state"
)

var errMigrationMapping = errors.New("invalid or incomplete application migration mapping")

// MigrationKeyMapping requires an explicit public kind. Omitted Kind means
// secret; omitted Name preserves the source key, without guessing a rename.
type MigrationKeyMapping struct {
	Source string `json:"source"`
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
}

type MigrationProfileMapping struct {
	Profile     string                `json:"profile"`
	Resource    string                `json:"resource"`
	Environment string                `json:"environment"`
	Keys        []MigrationKeyMapping `json:"keys"`
}

// ApplicationMigrationPlan is public metadata only. Every source key must be
// mapped exactly once, including explicitly mapping an empty source profile.
// The existing platform credential registry excludes all managed profiles.
type ApplicationMigrationPlan struct {
	Version   int                          `json:"version"`
	Revision  uint64                       `json:"source_revision"`
	Mappings  []MigrationProfileMapping    `json:"mappings"`
	Resources []policy.ApplicationResource `json:"resources"`
	Values    int                          `json:"value_count"`
}

type MigrationValue struct {
	Resource    string
	Environment string
	Name        string
	Kind        string
	value       []byte
}

func (MigrationValue) Format(s fmt.State, _ rune) { io.WriteString(s, "[REDACTED]") }
func (MigrationValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("migration values cannot be serialized")
}
func (MigrationValue) MarshalText() ([]byte, error) {
	return nil, errors.New("migration values cannot be serialized")
}

// Bytes borrows exact logical bytes during WithApplicationMigrationValues only.
// Consumers send secrets through stdin and explicitly public variables through
// the provider's var-set contract. No consumer may retain this borrowed slice.
func (v MigrationValue) Bytes() []byte { return v.value }

func clearMigrationValues(values []MigrationValue) {
	for i := range values {
		clear(values[i].value)
		values[i].value = nil
	}
}

func planMigration(snapshot state.Snapshot, mappings []MigrationProfileMapping, resources []policy.ApplicationResource, base map[string]string) (ApplicationMigrationPlan, []MigrationValue, error) {
	var plan ApplicationMigrationPlan
	canonical, err := policy.CanonicalApplications(resources, base)
	if err != nil || len(canonical) == 0 {
		return plan, nil, errMigrationMapping
	}
	doc, err := decode(snapshot.Data)
	if err != nil {
		return plan, nil, errMigrationMapping
	}
	profiles := object(doc["profiles"])
	byID := make(map[string]policy.ApplicationResource, len(canonical))
	for _, resource := range canonical {
		byID[resource.ID] = resource
	}
	plan = ApplicationMigrationPlan{Version: 1, Revision: snapshot.Revision, Resources: canonical, Mappings: make([]MigrationProfileMapping, 0, len(mappings))}
	covered := map[string]map[string]bool{}
	targets := map[[3]string]bool{}
	values := []MigrationValue{}
	ok := false
	defer func() {
		if !ok {
			clearMigrationValues(values)
		}
	}()
	for _, mapping := range mappings {
		resource, exists := byID[mapping.Resource]
		if mapping.Environment == "" {
			mapping.Environment = "common"
		}
		if !exists || !slices.Contains(resource.Environments, mapping.Environment) || isManagedProfile(mapping.Profile) || profiles[mapping.Profile] == nil {
			return ApplicationMigrationPlan{}, nil, errMigrationMapping
		}
		source := object(object(profiles[mapping.Profile])["secrets"])
		if covered[mapping.Profile] == nil {
			covered[mapping.Profile] = map[string]bool{}
		} else if len(mapping.Keys) == 0 {
			return ApplicationMigrationPlan{}, nil, errMigrationMapping
		}
		if len(mapping.Keys) == 0 && len(source) != 0 {
			return ApplicationMigrationPlan{}, nil, errMigrationMapping
		}
		mapping.Keys = slices.Clone(mapping.Keys)
		for i, key := range mapping.Keys {
			value, exists := source[key.Source].(string)
			if !exists || covered[mapping.Profile][key.Source] || len(value) > 65536 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return ApplicationMigrationPlan{}, nil, errMigrationMapping
			}
			if key.Name == "" {
				key.Name = key.Source
			}
			if key.Kind == "" {
				key.Kind = "secret"
			}
			if resource.Keys[key.Name] != key.Kind || !policy.ApplicationName.MatchString(key.Name) || policy.ReservedApplicationKey(key.Name, base) {
				return ApplicationMigrationPlan{}, nil, errMigrationMapping
			}
			target := [3]string{mapping.Resource, mapping.Environment, key.Name}
			if targets[target] {
				return ApplicationMigrationPlan{}, nil, errMigrationMapping
			}
			targets[target] = true
			covered[mapping.Profile][key.Source] = true
			mapping.Keys[i] = key
			values = append(values, MigrationValue{mapping.Resource, mapping.Environment, key.Name, key.Kind, []byte(value)})
		}
		sort.Slice(mapping.Keys, func(i, j int) bool { return mapping.Keys[i].Source < mapping.Keys[j].Source })
		plan.Mappings = append(plan.Mappings, mapping)
	}
	for name, raw := range profiles {
		if isManagedProfile(name) {
			continue
		}
		seen, exists := covered[name]
		if !exists || len(seen) != len(object(object(raw)["secrets"])) {
			return ApplicationMigrationPlan{}, nil, errMigrationMapping
		}
	}
	// Validate each effective environment, including common fallback and exact
	// overrides. Use the execution stack bound and conservative 4KiB pages on
	// both supported 64-bit architectures. Empty values remain valid storage.
	for _, resource := range canonical {
		for _, environment := range resource.Environments {
			effective := map[string]MigrationValue{}
			for _, layer := range []string{"common", environment} {
				for _, v := range values {
					if v.Resource == resource.ID && v.Environment == layer {
						effective[v.Name] = v
					}
				}
			}
			input := privateio.Bootstrap{Protocol: 1, Environment: []privateio.EnvironmentEntry{}}
			for name, value := range base {
				input.Environment = append(input.Environment, privateio.EnvironmentEntry{Name: name, Value: value})
			}
			patterns := [][]byte{}
			for name, v := range effective {
				input.Environment = append(input.Environment, privateio.EnvironmentEntry{Name: name, Value: string(v.value)})
				if v.Kind == "secret" {
					patterns = append(patterns, v.value)
				}
			}
			if _, err := privateio.ValidateBootstrap(input, []string{"/bin/sh"}, 8<<20, 4096); err != nil {
				return ApplicationMigrationPlan{}, nil, errMigrationMapping
			}
			matcher, err := secretoutput.New(patterns)
			if err != nil {
				return ApplicationMigrationPlan{}, nil, errMigrationMapping
			}
			matcher.Destroy()
		}
	}
	sort.Slice(plan.Mappings, func(i, j int) bool {
		a, b := plan.Mappings[i], plan.Mappings[j]
		if a.Profile != b.Profile {
			return a.Profile < b.Profile
		}
		if a.Resource != b.Resource {
			return a.Resource < b.Resource
		}
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		return len(a.Keys) > 0 && len(b.Keys) > 0 && a.Keys[0].Source < b.Keys[0].Source
	})
	sort.Slice(values, func(i, j int) bool {
		a, b := values[i], values[j]
		if a.Resource != b.Resource {
			return a.Resource < b.Resource
		}
		if a.Environment != b.Environment {
			return a.Environment < b.Environment
		}
		return a.Name < b.Name
	})
	plan.Values = len(values)
	ok = true
	return plan, values, nil
}

func (c Controller) PrepareApplicationMigration(ctx context.Context, mappings []MigrationProfileMapping, resources []policy.ApplicationResource, base map[string]string) (ApplicationMigrationPlan, error) {
	var plan ApplicationMigrationPlan
	err := c.backend().WithSnapshot(ctx, nil, func(s state.Snapshot) error {
		var values []MigrationValue
		var err error
		plan, values, err = planMigration(s, mappings, resources, base)
		clearMigrationValues(values)
		return err
	})
	return plan, err
}

// WithApplicationMigrationValues revalidates the complete prepared metadata at
// the original fenced revision. The callback is private host migration code;
// values are borrowed only until return and can never enter a public plan.
func (c Controller) WithApplicationMigrationValues(ctx context.Context, operation string, plan ApplicationMigrationPlan, base map[string]string, use func([]MigrationValue) error) error {
	if plan.Version != 1 || plan.Revision == 0 || use == nil || !requestIDPattern.MatchString(operation) {
		return errMigrationMapping
	}
	return c.backend().WithSnapshot(ctx, &plan.Revision, func(s state.Snapshot) error {
		fence, err := c.readApplicationMigrationFence()
		if err != nil || fence == nil || fence.State != "pending" || fence.Operation != operation || fence.Revision != plan.Revision {
			return ErrApplicationMigration
		}
		current, values, err := planMigration(s, plan.Mappings, plan.Resources, base)
		defer clearMigrationValues(values)
		if err != nil || !reflect.DeepEqual(current, plan) {
			return errMigrationMapping
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return use(values)
	})
}
