package service

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
	"loki/internal/secret"
)

type secretRequest struct {
	Action           string  `json:"action"`
	Profile          *string `json:"profile"`
	Secret           *string `json:"secret"`
	Name             *string `json:"name"`
	ImportID         *string `json:"import_id"`
	Value            *string `json:"value"`
	Bytes            int     `json:"bytes"`
	Offset           int     `json:"offset"`
	Limit            int     `json:"limit"`
	RequestID        string  `json:"request_id"`
	ExpectedRevision uint64  `json:"expected_revision"`
}

func SecretHandlers(client RuntimeCaller) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{
		"secret_inspect": mcpserver.Typed(func(ctx context.Context, r secretRequest) (*mcp.CallToolResult, error) {
			request := map[string]any{}
			switch r.Action {
			case "profiles":
				request["operation"] = "list_profiles"
				request["offset"], request["limit"] = r.Offset, r.Limit
			case "imports":
				request["operation"] = "list_imports"
				request["offset"], request["limit"] = r.Offset, r.Limit
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
				request["offset"], request["limit"] = r.Offset, r.Limit
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
			request := map[string]any{
				"profile": name, "expected_revision": r.ExpectedRevision, "request_id": r.RequestID,
			}
			switch r.Action {
			case "create_profile":
				request["operation"] = "profile_create_request"
			case "import_staged":
				id, err := mcpserver.Require(r.ImportID, "import_id")
				if err != nil {
					return nil, err
				}
				request["operation"] = "import_staged_request"
				request["import_id"] = id
			case "set_public":
				key, err := mcpserver.Require(r.Name, "name")
				if err != nil {
					return nil, err
				}
				value, err := mcpserver.Require(r.Value, "value")
				if err != nil {
					return nil, err
				}
				request["operation"] = "public_value_set_request"
				request["secret"], request["value"] = key, value
			case "generate":
				key, err := mcpserver.Require(r.Secret, "secret")
				if err != nil {
					return nil, err
				}
				request["operation"] = "secret_generate_request"
				request["secret"], request["bytes"] = key, r.Bytes
			default:
				return nil, fault.Error("secret_write action must be create_profile, import_staged, set_public, or generate")
			}
			return runtimeObject(ctx, client, request)
		}),
		"secret_delete": mcpserver.Typed(func(ctx context.Context, r secretRequest) (*mcp.CallToolResult, error) {
			name, err := mcpserver.Require(r.Profile, "profile")
			if err != nil {
				return nil, err
			}
			request := map[string]any{
				"profile": name, "expected_revision": r.ExpectedRevision, "request_id": r.RequestID,
			}
			switch r.Action {
			case "profile":
				request["operation"] = "profile_remove_request"
			case "secret":
				key, err := mcpserver.Require(r.Secret, "secret")
				if err != nil {
					return nil, err
				}
				request["operation"] = "secret_remove_request"
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
type guardedProfileInput struct {
	Profile          string `json:"profile"`
	RequestID        string `json:"request_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}
type pageInput struct {
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}
type secretInput struct {
	Profile string  `json:"profile"`
	Secret  string  `json:"secret"`
	Value   *string `json:"value"`
	Bytes   *int    `json:"bytes"`
}
type guardedSecretInput struct {
	Profile          string  `json:"profile"`
	Secret           string  `json:"secret"`
	Value            *string `json:"value"`
	Bytes            *int    `json:"bytes"`
	RequestID        string  `json:"request_id"`
	ExpectedRevision uint64  `json:"expected_revision"`
}
type importInput struct {
	Profile string            `json:"profile"`
	ID      string            `json:"import_id"`
	Values  map[string]string `json:"values"`
}
type guardedImportInput struct {
	Profile          string `json:"profile"`
	ID               string `json:"import_id"`
	RequestID        string `json:"request_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

func SecretOperations(c secret.Controller) map[string]rpc.Operation {
	adminValueHandler := func(public bool) rpc.Handler {
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
		"init": {Grant: controlpolicy.HostAdministration, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) { return c.Initialize(ctx) }},
		"list_profiles": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r pageInput) (map[string]any, error) {
			return c.ProfilesPage(ctx, r.Offset, r.Limit)
		})},
		"list_imports": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(_ context.Context, r pageInput) (map[string]any, error) {
			return c.ListImportsPage(r.Offset, r.Limit)
		})},
		"get_profile": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r profileInput) (map[string]any, error) {
			return c.Profile(ctx, r.Profile)
		})},
		"profile_create": {Grant: controlpolicy.HostAdministration, Handle: runtimeTyped(func(ctx context.Context, r profileInput) (map[string]any, error) {
			return c.CreateProfile(ctx, r.Profile)
		})},
		"profile_remove": {Grant: controlpolicy.HostAdministration, Handle: runtimeTyped(func(ctx context.Context, r profileInput) (map[string]any, error) {
			return c.RemoveProfile(ctx, r.Profile)
		})},
		"import_env": {Grant: controlpolicy.HostAdministration, Handle: runtimeTyped(func(ctx context.Context, r importInput) (map[string]any, error) {
			return c.ImportValues(ctx, r.Profile, r.Values)
		})},
		"import_staged_env": {Grant: controlpolicy.HostAdministration, Handle: runtimeTyped(func(ctx context.Context, r importInput) (map[string]any, error) {
			return c.ImportStaged(ctx, r.Profile, r.ID)
		})},
		"secret_set":       {Grant: controlpolicy.HostAdministration, Handle: adminValueHandler(false)},
		"public_value_set": {Grant: controlpolicy.HostAdministration, Handle: adminValueHandler(true)},
		"secret_generate": {Grant: controlpolicy.HostAdministration, Handle: runtimeTyped(func(ctx context.Context, r secretInput) (map[string]any, error) {
			size := 32
			if r.Bytes != nil {
				size = *r.Bytes
			}
			return c.Generate(ctx, r.Profile, r.Secret, size)
		})},
		"secret_remove": {Grant: controlpolicy.HostAdministration, Handle: runtimeTyped(func(ctx context.Context, r secretInput) (map[string]any, error) {
			return c.RemoveSecret(ctx, r.Profile, r.Secret)
		})},

		"profile_create_request": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r guardedProfileInput) (map[string]any, error) {
			return c.CreateProfileRequest(ctx, r.Profile, r.RequestID, r.ExpectedRevision)
		})},
		"profile_remove_request": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r guardedProfileInput) (map[string]any, error) {
			return c.RemoveProfileRequest(ctx, r.Profile, r.RequestID, r.ExpectedRevision)
		})},
		"import_staged_request": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r guardedImportInput) (map[string]any, error) {
			return c.ImportStagedRequest(ctx, r.Profile, r.ID, r.RequestID, r.ExpectedRevision)
		})},
		"public_value_set_request": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r guardedSecretInput) (map[string]any, error) {
			if r.Value == nil {
				return nil, fault.Error("public configuration value is required")
			}
			return c.SetPublicRequest(ctx, r.Profile, r.Secret, *r.Value, r.RequestID, r.ExpectedRevision)
		})},
		"secret_generate_request": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r guardedSecretInput) (map[string]any, error) {
			size := 32
			if r.Bytes != nil {
				size = *r.Bytes
			}
			return c.GenerateRequest(ctx, r.Profile, r.Secret, r.RequestID, r.ExpectedRevision, size)
		})},
		"secret_remove_request": {Grant: controlpolicy.Agent, Handle: runtimeTyped(func(ctx context.Context, r guardedSecretInput) (map[string]any, error) {
			return c.RemoveSecretRequest(ctx, r.Profile, r.Secret, r.RequestID, r.ExpectedRevision)
		})},
	}
	return ops
}
