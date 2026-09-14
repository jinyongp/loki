package service

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"loki/internal/fault"
	"loki/internal/project"
	"loki/internal/rpc"
)

type projectRequest struct {
	Action       string  `json:"action"`
	CWD          string  `json:"cwd"`
	Workstream   *string `json:"workstream"`
	Filename     *string `json:"filename"`
	Content      *string `json:"content"`
	Expected     *string `json:"expected_sha256"`
	Goal         *string `json:"goal"`
	SlugBase     *string `json:"slug_base"`
	Slug         *string `json:"slug"`
	Depth        string  `json:"depth"`
	IntentKind   string  `json:"intent_source_kind"`
	IntentSource *string `json:"intent_source"`
}

func runtimeCWD(raw json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return "", fault.Error("invalid runtime arguments")
	}
	cwd := "."
	if value, ok := fields["cwd"]; ok {
		if string(value) == "null" || json.Unmarshal(value, &cwd) != nil {
			return "", fault.Error("project state cwd must be workspace-relative")
		}
	}
	if cwd == "" || strings.HasPrefix(cwd, "/") || strings.ContainsAny(cwd, "\\\x00") || slices.Contains(strings.Split(cwd, "/"), "..") {
		return "", fault.Error("project state cwd must be workspace-relative")
	}
	return cwd, nil
}
func runtimeRequired(value *string, name string) (string, error) {
	if value == nil || *value == "" || len(*value) > rpc.MaxBytes {
		return "", fault.Error(name + " is required or too large")
	}
	return *value, nil
}
func runtimeOptional(values ...*string) error {
	for _, value := range values {
		if value != nil && (*value == "" || len(*value) > rpc.MaxBytes) {
			return fault.Error("optional project state value is invalid")
		}
	}
	return nil
}

// These operations retain the Python delegated-agent permission boundary.
// Administrative registration/workflow operations are assembled separately.
func ProjectStateOperations(store *project.Store, tasks project.Tasks) map[string]rpc.Operation {
	return map[string]rpc.Operation{
		"project_task": {Permission: rpc.Agent, Timeout: 1805 * time.Second, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			cwd, err := runtimeCWD(raw)
			if err != nil {
				return nil, err
			}
			r, err := rpc.Decode[project.RawTaskRequest](raw)
			if err != nil {
				return nil, err
			}
			var shape struct {
				Arguments []*string `json:"arguments"`
			}
			if json.Unmarshal(raw, &shape) != nil {
				return nil, fault.Error("invalid task arguments")
			}
			for _, argument := range shape.Arguments {
				if argument == nil {
					return nil, fault.Error("task arguments must be strings")
				}
			}
			r.CWD = cwd
			return tasks.Raw(ctx, r)
		}},
		"task": {Permission: rpc.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			cwd, err := runtimeCWD(raw)
			if err != nil {
				return nil, err
			}
			r, err := rpc.Decode[project.TaskRequest](raw)
			if err != nil {
				return nil, err
			}
			r.CWD = cwd
			return tasks.Do(ctx, r)
		}},
		"project_state": {Permission: rpc.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			cwd, err := runtimeCWD(raw)
			if err != nil {
				return nil, err
			}
			r, err := rpc.Decode[projectRequest](raw)
			if err != nil {
				return nil, err
			}
			r.CWD = cwd
			switch r.Action {
			case "status":
				return store.Status(ctx, r.CWD)
			case "list":
				return store.List(ctx, r.CWD)
			case "read":
				filename, err := runtimeRequired(r.Filename, "filename")
				if err != nil {
					return nil, err
				}
				return store.ReadArtifact(ctx, r.CWD, r.Workstream, filename)
			case "init":
				goal, err := runtimeRequired(r.Goal, "goal")
				if err != nil {
					return nil, err
				}
				if err = runtimeOptional(r.Slug, r.SlugBase, r.IntentSource); err != nil {
					return nil, err
				}
				if _, err = runtimeRequired(&r.Depth, "depth"); err != nil {
					return nil, err
				}
				if _, err = runtimeRequired(&r.IntentKind, "intent_source_kind"); err != nil {
					return nil, err
				}
				return store.Initialize(ctx, r.CWD, project.InitRequest{Goal: goal, SlugBase: r.SlugBase, Slug: r.Slug, Depth: r.Depth, IntentKind: r.IntentKind, IntentSource: r.IntentSource})
			case "bind":
				slug, err := runtimeRequired(r.Workstream, "workstream")
				if err != nil {
					return nil, err
				}
				return store.Bind(ctx, r.CWD, slug)
			case "write":
				if err = runtimeOptional(r.Workstream, r.Expected); err != nil {
					return nil, err
				}
				filename, err := runtimeRequired(r.Filename, "filename")
				if err != nil {
					return nil, err
				}
				content, err := runtimeRequired(r.Content, "content")
				if err != nil {
					return nil, err
				}
				return store.WriteArtifact(ctx, r.CWD, r.Workstream, filename, content, r.Expected)
			default:
				return nil, fault.Error("project state action must be status, list, read, init, bind, or write")
			}
		}},
	}
}
