package secret

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"

	"loki/internal/fault"
	"loki/internal/project"
	"loki/internal/state"
)

type Controller struct {
	StateDirectory string
	InboxDirectory string
	Projects       *project.Store
}

func (c Controller) backend() state.Store {
	return state.Store{Dir: c.StateDirectory, Validate: Validate}
}
func publicStateError(err error) error {
	if errors.Is(err, state.ErrUninitialized) {
		return fault.Error(state.ErrUninitialized.Error())
	}
	if errors.Is(err, state.ErrDecrypt) || errors.Is(err, state.ErrConflict) {
		return fault.Error(err.Error())
	}
	return err
}
func (c Controller) Initialize(ctx context.Context) (map[string]any, error) {
	created, err := c.backend().Initialize(ctx, json.RawMessage(`{"version":1,"profiles":{},"projects":{}}`))
	if err != nil {
		return nil, publicStateError(err)
	}
	return map[string]any{"initialized": true, "created": created}, nil
}
func (c Controller) load(ctx context.Context) (document, error) {
	snapshot, err := c.backend().Load(ctx)
	if err != nil {
		return nil, publicStateError(err)
	}
	return decode(snapshot.Data)
}
func (c Controller) mutate(ctx context.Context, fn func(document) (map[string]any, error)) (map[string]any, error) {
	var result map[string]any
	_, err := c.backend().Update(ctx, nil, func(data json.RawMessage) (json.RawMessage, error) {
		doc, err := decode(data)
		if err != nil {
			return nil, err
		}
		result, err = fn(doc)
		if err != nil {
			return nil, err
		}
		return json.Marshal(doc)
	})
	if err != nil {
		return nil, publicStateError(err)
	}
	return result, nil
}
func profile(doc document, name string) (map[string]any, error) {
	p := object(object(doc["profiles"])[name])
	if p == nil {
		return nil, fault.Error("unknown profile")
	}
	return p, nil
}
func (c Controller) Profiles(ctx context.Context) (map[string]any, error) {
	doc, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	profiles := object(doc["profiles"])
	items := []map[string]any{}
	for _, name := range keys(profiles) {
		items = append(items, profileMetadata(name, object(profiles[name]), false))
	}
	return map[string]any{"profiles": items}, nil
}
func (c Controller) Profile(ctx context.Context, name string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	doc, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	p, err := profile(doc, name)
	if err != nil {
		return nil, err
	}
	return profileMetadata(name, p, true), nil
}
func profileMetadata(name string, p map[string]any, include bool) map[string]any {
	values, actions := object(p["secrets"]), object(p["actions"])
	empty := []string{}
	for _, key := range keys(values) {
		if values[key] == "" {
			empty = append(empty, key)
		}
	}
	out := map[string]any{"name": name, "secret_names": keys(values), "secret_count": len(values), "configured_secret_count": len(values) - len(empty), "empty_secret_names": empty, "actions": keys(actions)}
	if include {
		policies := map[string]any{}
		for _, key := range keys(actions) {
			a := object(actions[key])
			selected, _ := stringsArray(a["secrets"])
			required, _ := stringsArray(get(a, "required_secrets", []any{}))
			missing := []string{}
			for _, name := range required {
				if values[name] == nil || values[name] == "" {
					missing = append(missing, name)
				}
			}
			slices.Sort(missing)
			policies[key] = map[string]any{
				"command": a["command"], "cwd": a["cwd"], "secrets": a["secrets"], "required_secrets": required, "missing_required_secrets": missing, "ready": len(missing) == 0,
				"all_secrets": get(a, "all_secrets", len(selected) == 0), "materialize_env_file": a["materialize_env_file"], "materialize_env_path": a["materialize_env_path"],
				"dynamic_port": a["dynamic_port"], "singleton": get(a, "singleton", false), "lock_probe": a["lock_probe"], "local_callback": get(a, "local_callback", false),
				"public_environment": get(a, "public_environment", []any{}), "preview_environment": get(a, "preview_environment", map[string]any{}), "docker_access": get(a, "docker_access", false),
				"timeout_seconds": a["timeout_seconds"], "max_output_bytes": a["max_output_bytes"],
			}
		}
		out["action_policies"] = policies
	}
	return out
}
func (c Controller) CreateProfile(ctx context.Context, name string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		profiles := object(doc["profiles"])
		if _, ok := profiles[name]; ok {
			return nil, fault.Error("profile already exists")
		}
		profiles[name] = map[string]any{"secrets": map[string]any{}, "actions": map[string]any{}}
		return map[string]any{"profile": name, "created": true}, nil
	})
}
func (c Controller) RemoveProfile(ctx context.Context, name string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		if _, err := profile(doc, name); err != nil {
			return nil, err
		}
		delete(object(doc["profiles"]), name)
		return map[string]any{"profile": name, "removed": true}, nil
	})
}
func (c Controller) ImportValues(ctx context.Context, name string, values map[string]string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, fault.Error("dotenv import must contain secrets")
	}
	for key := range values {
		if err := SecretName(key); err != nil {
			return nil, err
		}
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		p, err := profile(doc, name)
		if err != nil {
			return nil, err
		}
		secrets := object(p["secrets"])
		names := []string{}
		for key, value := range values {
			secrets[key] = value
			names = append(names, key)
		}
		slices.Sort(names)
		return map[string]any{"profile": name, "imported": names, "count": len(names)}, nil
	})
}
func (c Controller) SetSecret(ctx context.Context, name, key, value string, public bool) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	if public {
		for _, r := range value {
			if r == 0 {
				return nil, fault.Error("public configuration value contains NUL")
			}
		}
		if len(value) > 65536 {
			return nil, fault.Error("public configuration value exceeds 65536 bytes")
		}
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		p, err := profile(doc, name)
		if err != nil {
			return nil, err
		}
		object(p["secrets"])[key] = value
		return map[string]any{"profile": name, "secret": key, "stored": true}, nil
	})
}
func (c Controller) Generate(ctx context.Context, name, key string, count int) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	if count < 16 || count > 128 {
		return nil, fault.Error("generated secret size must be between 16 and 128 bytes")
	}
	bytes := make([]byte, count)
	if _, err := rand.Read(bytes); err != nil {
		return nil, err
	}
	value := base64.RawURLEncoding.EncodeToString(bytes)
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		p, err := profile(doc, name)
		if err != nil {
			return nil, err
		}
		values := object(p["secrets"])
		if v, exists := values[key]; exists && v != "" {
			return nil, fault.Error("secret is already configured")
		}
		values[key] = value
		return map[string]any{"profile": name, "secret": key, "generated": true, "bytes": count}, nil
	})
}
func (c Controller) RemoveSecret(ctx context.Context, name, key string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := SecretName(key); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		p := object(object(doc["profiles"])[name])
		values := object(p["secrets"])
		if _, exists := values[key]; !exists {
			return nil, fault.Error("unknown secret")
		}
		delete(values, key)
		for _, raw := range object(p["actions"]) {
			a := object(raw)
			selected, _ := stringsArray(a["secrets"])
			required, _ := stringsArray(get(a, "required_secrets", []any{}))
			if slices.Contains(selected, key) || slices.Contains(required, key) {
				return nil, fault.Error("secret is still referenced by an action")
			}
		}
		return map[string]any{"profile": name, "secret": key, "removed": true}, nil
	})
}
func (c Controller) SetAction(ctx context.Context, name, key string, raw json.RawMessage) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := ActionName(key); err != nil {
		return nil, err
	}
	a, err := decodeObject(raw)
	if err != nil {
		return nil, fault.Error("action policy is invalid")
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		p, err := profile(doc, name)
		if err != nil {
			return nil, err
		}
		if err = validateAction(a, object(p["secrets"])); err != nil {
			return nil, err
		}
		object(p["actions"])[key] = a
		return map[string]any{"profile": name, "action": key, "stored": true}, nil
	})
}
func (c Controller) RemoveAction(ctx context.Context, name, key string) (map[string]any, error) {
	if err := ProfileName(name); err != nil {
		return nil, err
	}
	if err := ActionName(key); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		p := object(object(doc["profiles"])[name])
		actions := object(p["actions"])
		if _, exists := actions[key]; !exists {
			return nil, fault.Error("unknown action")
		}
		delete(actions, key)
		return map[string]any{"profile": name, "action": key, "removed": true}, nil
	})
}

func (c Controller) Register(ctx context.Context, cwd string, name *string) (map[string]any, error) {
	id, err := c.Projects.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	resolved := filepath.Base(id.RepositoryRoot)
	if name != nil {
		resolved = *name
	}
	if !profilePattern.MatchString(resolved) {
		return nil, fault.Error("project name is invalid")
	}
	repository := c.Projects.Metadata(id)["repository"]
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		projects := object(doc["projects"])
		p := object(projects[id.ProjectID])
		created := p == nil
		if created {
			p = map[string]any{"workflows": map[string]any{}}
			projects[id.ProjectID] = p
		}
		p["name"] = resolved
		p["repository"] = repository
		return map[string]any{"project_id": id.ProjectID, "name": resolved, "repository": repository, "created": created}, nil
	})
}
func (c Controller) Unregister(ctx context.Context, cwd string) (map[string]any, error) {
	id, err := c.Projects.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		projects := object(doc["projects"])
		if _, exists := projects[id.ProjectID]; !exists {
			return nil, fault.Error("project is not registered")
		}
		delete(projects, id.ProjectID)
		return map[string]any{"project_id": id.ProjectID, "unregistered": true}, nil
	})
}
func (c Controller) Registration(ctx context.Context, cwd string) (map[string]any, error) {
	id, err := c.Projects.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	doc, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"project_id": id.ProjectID, "worktree_id": id.WorktreeID, "cwd": filepath.ToSlash(filepath.Clean(cwd))}
	p := object(object(doc["projects"])[id.ProjectID])
	out["registered"] = p != nil
	if p == nil {
		out["workflows"] = []string{}
	} else {
		out["name"] = p["name"]
		out["repository"] = p["repository"]
		out["workflows"] = keys(object(p["workflows"]))
	}
	return out, nil
}
func (c Controller) workflowRepository(ctx context.Context, workflow map[string]any, profiles map[string]any, id string) error {
	steps, _ := array(workflow["steps"])
	for _, raw := range steps {
		pair, _ := stringsArray(raw)
		action := object(object(object(profiles[pair[0]])["actions"])[pair[1]])
		cwd, _ := text(action["cwd"])
		identity, err := c.Projects.Resolve(ctx, cwd)
		if err != nil {
			return err
		}
		if identity.ProjectID != id {
			return fault.Error("workflow action belongs to another Git repository")
		}
	}
	return nil
}
func workflowName(name string) error {
	if !profilePattern.MatchString(name) {
		return fault.Error("workflow name is invalid")
	}
	return nil
}
func (c Controller) SetWorkflow(ctx context.Context, cwd, name string, raw json.RawMessage) (map[string]any, error) {
	id, err := c.Projects.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if err = workflowName(name); err != nil {
		return nil, err
	}
	w, err := decodeObject(raw)
	if err != nil {
		return nil, fault.Error("project workflow policy is invalid")
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		p := object(object(doc["projects"])[id.ProjectID])
		if p == nil {
			return nil, fault.Error("project is not registered")
		}
		profiles := object(doc["profiles"])
		if err := validateWorkflow(w, profiles); err != nil {
			return nil, err
		}
		if err := c.workflowRepository(ctx, w, profiles, id.ProjectID); err != nil {
			return nil, err
		}
		object(p["workflows"])[name] = w
		return map[string]any{"project_id": id.ProjectID, "workflow": name, "stored": true, "steps": w["steps"]}, nil
	})
}
func (c Controller) RemoveWorkflow(ctx context.Context, cwd, name string) (map[string]any, error) {
	id, err := c.Projects.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if err = workflowName(name); err != nil {
		return nil, err
	}
	return c.mutate(ctx, func(doc document) (map[string]any, error) {
		p := object(object(doc["projects"])[id.ProjectID])
		workflows := object(p["workflows"])
		if _, exists := workflows[name]; !exists {
			return nil, fault.Error("project workflow is not registered")
		}
		delete(workflows, name)
		return map[string]any{"project_id": id.ProjectID, "workflow": name, "removed": true}, nil
	})
}
func (c Controller) Workflow(ctx context.Context, cwd, name string) (map[string]any, error) {
	id, err := c.Projects.Resolve(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if err = workflowName(name); err != nil {
		return nil, err
	}
	doc, err := c.load(ctx)
	if err != nil {
		return nil, err
	}
	p := object(object(doc["projects"])[id.ProjectID])
	if p == nil {
		return nil, fault.Error("project is not registered")
	}
	w := object(object(p["workflows"])[name])
	if w == nil {
		return nil, fault.Error("project workflow is not registered")
	}
	if err = c.workflowRepository(ctx, w, object(doc["profiles"]), id.ProjectID); err != nil {
		return nil, err
	}
	out := map[string]any{"project_id": id.ProjectID, "workflow": name, "cwd": filepath.ToSlash(filepath.Clean(cwd))}
	for key, value := range w {
		out[key] = value
	}
	return out, nil
}
