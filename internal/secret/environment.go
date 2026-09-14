package secret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
)

const maxEnvironmentSecrets = 256

// EnvironmentPlan is a private runtime-only snapshot for one approved process.
// It cannot cross a JSON or formatting boundary.
type EnvironmentPlan struct {
	profile string
	values  map[string]string
	names   []string
}

func (p EnvironmentPlan) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private environment plan]")
}

func (p EnvironmentPlan) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private environment plan cannot be serialized")
}

func (p EnvironmentPlan) Names() []string {
	return slices.Clone(p.names)
}

func (p EnvironmentPlan) Entries() []string {
	entries := make([]string, 0, len(p.names))
	for _, name := range p.names {
		entries = append(entries, name+"="+p.values[name])
	}
	return entries
}

func (p EnvironmentPlan) RedactionValues() []string {
	values := make([]string, 0, len(p.names))
	for _, name := range p.names {
		if value := p.values[name]; value != "" {
			values = append(values, value)
		}
	}
	return values
}

// ResolveEnvironment decrypts only the explicitly selected vault entries.
// Callers must keep the returned plan inside the privileged runtime.
func (c Controller) ResolveEnvironment(ctx context.Context, profileName string, requested []string) (EnvironmentPlan, error) {
	var plan EnvironmentPlan
	if err := ProfileName(profileName); err != nil {
		return plan, err
	}
	if len(requested) > maxEnvironmentSecrets {
		return plan, errors.New("too many requested environment secrets")
	}
	names := slices.Clone(requested)
	slices.Sort(names)
	for index, name := range names {
		if err := SecretName(name); err != nil {
			return plan, err
		}
		if index > 0 && name == names[index-1] {
			return plan, errors.New("duplicate requested environment secret")
		}
	}
	doc, err := c.load(ctx)
	if err != nil {
		return plan, err
	}
	profile, err := profile(doc, profileName)
	if err != nil {
		return plan, err
	}
	stored := object(profile["secrets"])
	values := make(map[string]string, len(names))
	for _, name := range names {
		value, ok := stored[name].(string)
		if !ok || value == "" {
			return plan, errors.New("requested environment secret is not configured")
		}
		values[name] = value
	}
	return EnvironmentPlan{profile: profileName, values: values, names: names}, nil
}
