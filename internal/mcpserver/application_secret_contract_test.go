package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestApplicationMutationContractRejectsLegacyBeforeHandler(t *testing.T) {
	handlers := testHandlers(t)
	calls := 0
	for _, tool := range []string{"secret_write", "secret_delete"} {
		handlers[tool] = func(_ context.Context, input map[string]any) (*mcp.CallToolResult, error) {
			calls++
			result := map[string]any{"action": input["action"], "resource": input["resource"], "env": "common", "revision": strings.Repeat("b", 64), "request_id": input["request_id"], "changed": true, "replayed": false}
			switch input["action"] {
			case "generate":
				result["name"], result["bytes"] = input["name"], 32
			case "set_public":
				result["name"] = input["name"]
			case "import_staged":
				result["stage_id"], result["count"] = input["stage_id"], 1
			case "unset":
				result["name"], result["kind"], result["effective_present"], result["source"] = input["name"], input["kind"], true, "common"
			}
			return Object(result)
		}
	}
	client := connect(t, handlers)
	for _, action := range []string{"generate", "set_public", "import_staged", "unset"} {
		tool := "secret_write"
		args := map[string]any{"action": action, "resource": "example", "expected_revision": strings.Repeat("a", 64), "request_id": "88000000-0000-4000-8000-000000000001"}
		if action == "import_staged" {
			args["stage_id"] = strings.Repeat("a", 32)
		} else {
			args["name"] = "TOKEN"
		}
		if action == "set_public" {
			args["value"] = "public-configuration"
		}
		if action == "unset" {
			tool = "secret_delete"
			args["kind"] = "secret"
		}
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil || result.IsError {
			t.Fatal("valid application mutation refused", action, err)
		}
		before := calls
		for field, value := range map[string]any{"profile": "github-app", "bytes": 128, "expected_revision": 1, "env": nil, "unknown": true} {
			invalid := map[string]any{}
			for k, v := range args {
				invalid[k] = v
			}
			invalid[field] = value
			result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: tool, Arguments: invalid})
			if err != nil || !result.IsError || calls != before {
				t.Fatal("invalid request reached handler", action, field, err)
			}
		}
	}
}
