package tools

import "fmt"

type Readiness string

const (
	Unknown  Readiness = "unknown"
	Ready    Readiness = "ready"
	Degraded Readiness = "degraded"
)

// State describes observed resources and desired public exposure independently.
// Dependencies may be ready as private services while their public group is off.
type State struct {
	Installed bool      `json:"installed"`
	Release   string    `json:"release,omitempty"`
	Enabled   bool      `json:"enabled"`
	Readiness Readiness `json:"readiness"`
	Reason    string    `json:"reason,omitempty"`
}

func (s State) Validate() error {
	if s.Readiness != Unknown && s.Readiness != Ready && s.Readiness != Degraded {
		return fmt.Errorf("invalid tool readiness %q", s.Readiness)
	}
	if s.Installed {
		if !releasePattern.MatchString(s.Release) {
			return fmt.Errorf("installed tool requires an exact 0.2.x release")
		}
	} else if s.Release != "" || s.Enabled || s.Readiness != Unknown {
		return fmt.Errorf("absent tool cannot be enabled, ready or bound to a release")
	}
	return nil
}

// AvailableBindings computes discovery from a current resource-state snapshot.
// Host composition must also authorize each call against current state.
func (r Resolution) AvailableBindings(states map[ID]State) ([]Binding, error) {
	ready := map[ID]bool{}
	for _, manifest := range r.Ordered {
		state, exists := states[manifest.ID]
		if !exists {
			continue
		}
		if err := state.Validate(); err != nil {
			return nil, fmt.Errorf("tool %s: %w", manifest.ID, err)
		}
		usable := state.Installed && state.Release == manifest.Release && state.Readiness == Ready
		for _, dependency := range manifest.Requires {
			usable = usable && ready[dependency]
		}
		ready[manifest.ID] = usable
	}
	var bindings []Binding
	for _, binding := range r.Bindings {
		if ready[binding.Module] && states[binding.Module].Enabled {
			bindings = append(bindings, binding)
		}
	}
	return bindings, nil
}

// AuthorizeBinding uses a fresh snapshot for every invocation, including names
// previously cached by a client. Discovery alone is not an authorization grant.
func (r Resolution) AuthorizeBinding(states map[ID]State, name string) error {
	bindings, err := r.AvailableBindings(states)
	if err != nil {
		return err
	}
	for _, binding := range bindings {
		if binding.Name == name {
			return nil
		}
	}
	return fmt.Errorf("tool binding %q is disabled, unavailable or unselected", name)
}
