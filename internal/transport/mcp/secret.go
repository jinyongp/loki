package mcptransport

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/mcpserver"
	"loki/internal/rpc"
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

func SecretHandlers(client rpc.Caller) map[string]mcpserver.Handler {
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
