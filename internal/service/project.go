package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/project"
	"loki/internal/rpc"
)

type RuntimeCaller interface {
	Call(context.Context, any) (json.RawMessage, error)
}

type projectRequest struct {
	Action          string   `json:"action"`
	CWD             string   `json:"cwd"`
	Workstream      *string  `json:"workstream"`
	Filename        *string  `json:"filename"`
	Content         *string  `json:"content"`
	Expected        *string  `json:"expected_sha256"`
	Goal            *string  `json:"goal"`
	SlugBase        *string  `json:"slug_base"`
	Slug            *string  `json:"slug"`
	Depth           string   `json:"depth"`
	IntentKind      string   `json:"intent_source_kind"`
	IntentSource    *string  `json:"intent_source"`
	Name            *string  `json:"name"`
	Workflow        *string  `json:"workflow"`
	Steps           []string `json:"steps"`
	RequiredSecrets []string `json:"required_secrets"`
	TimeoutSeconds  int      `json:"timeout_seconds"`
}

func relativeCWD(paths *policy.Workspace, cwd string) (string, error) {
	full, err := paths.ResolveCWD(cwd)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(paths.Root(), full)
	return filepath.ToSlash(rel), err
}
func runtimeObject(ctx context.Context, client RuntimeCaller, request any) (*mcp.CallToolResult, error) {
	data, err := client.Call(ctx, request)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err = decoder.Decode(&result); err != nil || result == nil {
		return nil, fault.Error("invalid runtime response")
	}
	return objectResult(result, nil)
}

// ProjectHandlers only forwards central operations over the authenticated
// runtime socket. The MCP user never receives access to the central state root.
func ProjectHandlers(client RuntimeCaller, paths *policy.Workspace) map[string]mcpserver.Handler {
	task := func(allowed []string, forcedAction string) mcpserver.Handler {
		return mcpserver.Typed(func(ctx context.Context, r project.TaskRequest) (*mcp.CallToolResult, error) {
			if forcedAction != "" {
				r.Action = forcedAction
			}
			if !slices.Contains(allowed, r.Action) {
				return nil, fault.Error("invalid task action")
			}
			var err error
			r.CWD, err = relativeCWD(paths, r.CWD)
			if err != nil {
				return nil, err
			}
			return runtimeObject(ctx, client, struct {
				Operation string `json:"operation"`
				project.TaskRequest
			}{"task", r})
		})
	}
	return map[string]mcpserver.Handler{
		"task_inspect": task([]string{"status", "diagnostics", "list", "next", "get", "count"}, ""),
		"task_write":   task([]string{"add", "modify", "annotate", "start", "stop", "done"}, ""),
		"task_delete":  task([]string{"delete"}, "delete"),
		"project": mcpserver.Typed(func(ctx context.Context, r projectRequest) (*mcp.CallToolResult, error) {
			cwd, err := relativeCWD(paths, r.CWD)
			if err != nil {
				return nil, err
			}
			r.CWD = cwd
			if slices.Contains([]string{"status", "list", "read", "init", "bind", "write"}, r.Action) {
				missing := ""
				switch r.Action {
				case "read":
					if r.Filename == nil {
						missing = "filename"
					}
				case "init":
					if r.Goal == nil {
						missing = "goal"
					}
				case "bind":
					if r.Workstream == nil {
						missing = "workstream"
					}
				case "write":
					if r.Filename == nil || r.Content == nil {
						missing = "filename and content"
					}
				}
				if missing != "" {
					return nil, fault.Error(missing + " is required for project_state action=" + r.Action)
				}
				return runtimeObject(ctx, client, struct {
					Operation string `json:"operation"`
					projectRequest
				}{"project_state", r})
			}
			request := map[string]any{"cwd": cwd}
			switch r.Action {
			case "register":
				request["operation"] = "project_register"
				request["name"] = r.Name
			case "unregister":
				request["operation"] = "project_unregister"
			case "registration":
				request["operation"] = "project_status"
			case "workflow", "set_workflow", "remove_workflow":
				if r.Workflow == nil {
					return nil, fault.Error("workflow is required for project action=" + r.Action)
				}
				request["operation"] = "project_" + r.Action
				request["workflow"] = *r.Workflow
				if r.Action == "set_workflow" {
					steps := [][]string{}
					for _, value := range r.Steps {
						reference, err := splitReference(value, "step")
						if err != nil {
							return nil, err
						}
						steps = append(steps, reference)
					}
					if len(steps) == 0 {
						return nil, fault.Error("steps are required for project action=set_workflow")
					}
					grouped := map[string][]string{}
					for _, value := range r.RequiredSecrets {
						reference, err := splitReference(value, "required secret")
						if err != nil {
							return nil, err
						}
						grouped[reference[0]] = append(grouped[reference[0]], reference[1])
					}
					request["steps"] = steps
					request["required_secrets"] = grouped
					request["timeout_seconds"] = r.TimeoutSeconds
				}
			default:
				return nil, fault.Error("project_state action must be status, list, read, init, bind, or write")
			}
			return runtimeObject(ctx, client, request)
		}),
	}
}
func splitReference(value, label string) ([]string, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fault.Error(label + " must use PROFILE/NAME")
	}
	return parts, nil
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
