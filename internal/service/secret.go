package service

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
	"loki/internal/secret"
)

type secretRequest struct {
	Action   string  `json:"action"`
	Profile  *string `json:"profile"`
	Secret   *string `json:"secret"`
	ImportID *string `json:"import_id"`
	Value    *string `json:"value"`
	Bytes    int     `json:"bytes"`
	Limit    int     `json:"limit"`
}

func SecretHandlers(client RuntimeCaller) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"secret_inspect": mcpserver.Typed(func(ctx context.Context, r secretRequest) (*mcp.CallToolResult, error) {
			request := map[string]any{}
			switch r.Action {
			case "profiles":
				request["operation"] = "list_profiles"
			case "imports":
				request["operation"] = "list_imports"
			case "profile":
				name, err := mcpserver.Require(r.Profile, "profile")
				if err != nil {
					return nil, err
				}
				request["operation"] = "get_profile"
				request["profile"] = name
			case "status":
				request["operation"] = "status"
			case "audit":
				request["operation"] = "audit"
				request["limit"] = r.Limit
			default:
				return nil, fault.Error("secret_inspect action must be profiles, imports, profile, status, or audit")
			}
			return runtimeObject(ctx, client, request)
		}),
		"secret_write": mcpserver.Typed(func(ctx context.Context, r secretRequest) (*mcp.CallToolResult, error) {
			name, err := mcpserver.Require(r.Profile, "profile")
			if err != nil {
				return nil, err
			}
			request := map[string]any{"profile": name}
			switch r.Action {
			case "create_profile":
				request["operation"] = "profile_create"
			case "import_env":
				id, err := mcpserver.Require(r.ImportID, "import_id")
				if err != nil {
					return nil, err
				}
				request["operation"] = "import_staged_env"
				request["import_id"] = id
			case "set", "generate":
				key, err := mcpserver.Require(r.Secret, "secret")
				if err != nil {
					return nil, err
				}
				request["secret"] = key
				if r.Action == "set" {
					value, err := mcpserver.Require(r.Value, "value")
					if err != nil {
						return nil, err
					}
					request["operation"] = "public_value_set"
					request["value"] = value
				} else {
					request["operation"] = "secret_generate"
					request["bytes"] = r.Bytes
				}
			default:
				return nil, fault.Error("secret_write action must be create_profile, import_env, set, or generate")
			}
			return runtimeObject(ctx, client, request)
		}),
		"secret_delete": mcpserver.Typed(func(ctx context.Context, r secretRequest) (*mcp.CallToolResult, error) {
			name, err := mcpserver.Require(r.Profile, "profile")
			if err != nil {
				return nil, err
			}
			request := map[string]any{"profile": name}
			switch r.Action {
			case "profile":
				request["operation"] = "profile_remove"
			case "secret":
				key, err := mcpserver.Require(r.Secret, "secret")
				if err != nil {
					return nil, err
				}
				request["operation"] = "secret_remove"
				request["secret"] = key
			default:
				return nil, fault.Error("secret_delete action must be secret or profile")
			}
			return runtimeObject(ctx, client, request)
		}),
	}
}

func runtimeTyped[T any](handler func(context.Context, T) (map[string]any, error)) rpc.Handler {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		r, err := rpc.Decode[T](raw)
		if err != nil {
			return nil, err
		}
		return handler(ctx, r)
	}
}

type profileInput struct {
	Profile string `json:"profile"`
}
type secretInput struct {
	Profile string  `json:"profile"`
	Secret  string  `json:"secret"`
	Value   *string `json:"value"`
	Bytes   *int    `json:"bytes"`
}
type importInput struct {
	Profile string            `json:"profile"`
	ID      string            `json:"import_id"`
	Values  map[string]string `json:"values"`
}
type policyInput struct {
	Profile string          `json:"profile"`
	Name    string          `json:"action_name"`
	Action  json.RawMessage `json:"action"`
}
type registrationInput struct {
	Name     *string         `json:"name"`
	Workflow *string         `json:"workflow"`
	Steps    json.RawMessage `json:"steps"`
	Required json.RawMessage `json:"required_secrets"`
	Timeout  json.RawMessage `json:"timeout_seconds"`
}

func SecretOperations(c secret.Controller) map[string]rpc.Operation {
	valueHandler := func(public bool) rpc.Handler {
		return runtimeTyped(func(ctx context.Context, r secretInput) (map[string]any, error) {
			if err := secret.ProfileName(r.Profile); err != nil {
				return nil, err
			}
			if err := secret.SecretName(r.Secret); err != nil {
				return nil, err
			}
			if r.Value == nil {
				if public {
					return nil, fault.Error("public configuration value is required")
				}
				return nil, fault.Error("secret value is required")
			}
			return c.SetSecret(ctx, r.Profile, r.Secret, *r.Value, public)
		})
	}
	ops := map[string]rpc.Operation{
		"init":          {Permission: rpc.Administrative, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) { return c.Initialize(ctx) }},
		"list_profiles": {Permission: rpc.Agent, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) { return c.Profiles(ctx) }},
		"list_imports":  {Permission: rpc.Agent, Handle: func(context.Context, json.RawMessage) (any, error) { return c.ListImports() }},
		"get_profile":   {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r profileInput) (map[string]any, error) { return c.Profile(ctx, r.Profile) })},
		"profile_create": {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r profileInput) (map[string]any, error) {
			return c.CreateProfile(ctx, r.Profile)
		})},
		"profile_remove": {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r profileInput) (map[string]any, error) {
			return c.RemoveProfile(ctx, r.Profile)
		})},
		"import_env": {Permission: rpc.Administrative, Handle: runtimeTyped(func(ctx context.Context, r importInput) (map[string]any, error) {
			return c.ImportValues(ctx, r.Profile, r.Values)
		})},
		"import_staged_env": {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r importInput) (map[string]any, error) {
			return c.ImportStaged(ctx, r.Profile, r.ID)
		})},
		"secret_set":       {Permission: rpc.Administrative, Handle: valueHandler(false)},
		"public_value_set": {Permission: rpc.Agent, Handle: valueHandler(true)},
		"secret_generate": {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r secretInput) (map[string]any, error) {
			size := 32
			if r.Bytes != nil {
				size = *r.Bytes
			}
			return c.Generate(ctx, r.Profile, r.Secret, size)
		})},
		"secret_remove": {Permission: rpc.Agent, Handle: runtimeTyped(func(ctx context.Context, r secretInput) (map[string]any, error) {
			return c.RemoveSecret(ctx, r.Profile, r.Secret)
		})},
	}
	return ops
}
