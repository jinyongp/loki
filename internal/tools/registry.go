package tools

import (
	"fmt"
	"slices"
)

type Registry struct {
	modules map[ID]Manifest
}

// NewRegistry validates an explicit composition contract before resolution.
func NewRegistry(manifests []Manifest) (*Registry, error) {
	registry := &Registry{modules: make(map[ID]Manifest, len(manifests))}
	var release string
	var contract string
	for _, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			return nil, err
		}
		if _, exists := registry.modules[manifest.ID]; exists {
			return nil, fmt.Errorf("duplicate tool ID %q", manifest.ID)
		}
		if release != "" && !CompatibleComposition(contract, release, manifest) {
			return nil, fmt.Errorf("tool %s belongs to a different release train", manifest.ID)
		}
		release = manifest.Release
		contract = manifest.Contract
		registry.modules[manifest.ID] = cloneManifest(manifest)
	}
	ids := make([]ID, 0, len(registry.modules))
	for id := range registry.modules {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	visited, active := map[ID]bool{}, map[ID]bool{}
	var visit func(ID) error
	visit = func(id ID) error {
		if active[id] {
			return fmt.Errorf("tool prerequisite cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		module, exists := registry.modules[id]
		if !exists {
			return fmt.Errorf("unknown tool prerequisite %q", id)
		}
		active[id] = true
		dependencies := append([]ID(nil), module.Requires...)
		slices.Sort(dependencies)
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		active[id] = false
		visited[id] = true
		return nil
	}
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

type Binding struct {
	Module ID     `json:"module"`
	Name   string `json:"name"`
}

// Resolution is an installation/composition plan, not an activation grant.
// Ordered includes private prerequisites; Bindings includes selected modules only.
type Resolution struct {
	Target   Target     `json:"target"`
	Selected []ID       `json:"selected"`
	Ordered  []Manifest `json:"ordered"`
	Bindings []Binding  `json:"bindings"`
}

func (r *Registry) Resolve(target Target, selected []ID) (Resolution, error) {
	return r.resolve(target, selected, true)
}

// ResolveInstallation computes acquisition only. Private prerequisites and
// installed-but-disabled modules do not claim public binding names.
func (r *Registry) ResolveInstallation(target Target, selected []ID) (Resolution, error) {
	return r.resolve(target, selected, false)
}

func (r *Registry) resolve(target Target, selected []ID, public bool) (Resolution, error) {
	if err := target.Validate(); err != nil {
		return Resolution{}, err
	}
	if r == nil {
		return Resolution{}, fmt.Errorf("tool registry is not configured")
	}
	result := Resolution{Target: target, Selected: append([]ID(nil), selected...)}
	slices.Sort(result.Selected)
	requested, visited := map[ID]bool{}, map[ID]bool{}
	for _, id := range result.Selected {
		if requested[id] {
			return Resolution{}, fmt.Errorf("duplicate selected tool %q", id)
		}
		if _, exists := r.modules[id]; !exists {
			return Resolution{}, fmt.Errorf("unknown selected tool %q", id)
		}
		requested[id] = true
	}
	var visit func(ID) error
	visit = func(id ID) error {
		if visited[id] {
			return nil
		}
		manifest := r.modules[id]
		if !slices.Contains(manifest.Targets, target) {
			return fmt.Errorf("tool %s does not support %s/%s/%s", id, target.OS, target.Arch, target.Mode)
		}
		dependencies := append([]ID(nil), manifest.Requires...)
		slices.Sort(dependencies)
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visited[id] = true
		result.Ordered = append(result.Ordered, cloneManifest(manifest))
		return nil
	}
	owners := map[string]ID{}
	for _, id := range result.Selected {
		if err := visit(id); err != nil {
			return Resolution{}, err
		}
		if !public {
			continue
		}
		names := append([]string(nil), r.modules[id].Tools...)
		slices.Sort(names)
		for _, name := range names {
			if owner, exists := owners[name]; exists {
				return Resolution{}, fmt.Errorf("MCP binding %q is provided by both %s and %s", name, owner, id)
			}
			owners[name] = id
			result.Bindings = append(result.Bindings, Binding{Module: id, Name: name})
		}
	}
	return result, nil
}
