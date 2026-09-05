package secret

import (
	"context"
	"slices"
)

// BootstrapPlan contains public readiness metadata only. The helper resolves
// each registered action through the runtime; no secret enters its argv/env.
type BootstrapPlan struct {
	Metadata       map[string]any
	TimeoutSeconds int
}

func (c Controller) BootstrapPreflight(ctx context.Context, cwd, name string) (BootstrapPlan, error) {
	w, err := c.Workflow(ctx, cwd, name)
	if err != nil {
		return BootstrapPlan{}, err
	}
	doc, err := c.load(ctx)
	if err != nil {
		return BootstrapPlan{}, err
	}
	missing := map[string][]string{}
	for profile, required := range object(w["required_secrets"]) {
		values := object(object(object(doc["profiles"])[profile])["secrets"])
		names, _ := stringsArray(required)
		for _, name := range names {
			if values[name] == nil || values[name] == "" {
				missing[profile] = append(missing[profile], name)
			}
		}
		slices.Sort(missing[profile])
	}
	timeout, _ := number(w["timeout_seconds"], false)
	return BootstrapPlan{Metadata: map[string]any{
		"project_id": w["project_id"], "cwd": w["cwd"], "workflow": name,
		"missing_required_secrets": missing, "configuration_ready": len(missing) == 0,
	}, TimeoutSeconds: int(timeout)}, nil
}
