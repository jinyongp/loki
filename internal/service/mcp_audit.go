package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/audit"
	"loki/internal/mcpserver"
)

// Metadata is an explicit allowlist. Never serialize the entire tool request
// or result: both may contain file contents, typed credentials or response bodies.
func auditMetadata(args map[string]any) map[string]any {
	metadata := map[string]any{}
	budget := 8192
	for _, key := range []string{"action", "operation", "cwd", "path", "source", "destination", "profile", "action_name", "name", "scope", "session_id", "share_id", "kind", "request_id", "port", "full_page", "ttl_seconds", "overwrite", "index", "staged", "reverse", "regex"} {
		v, exists := args[key]
		if !exists {
			continue
		}
		switch v := v.(type) {
		case nil, bool, int, int64, float64, json.Number:
			metadata[key] = v
		case string:
			if len(v) <= min(1024, budget) {
				metadata[key] = v
				budget -= len(v)
			}
		}
	}
	return metadata
}
func auditHandler(log *audit.Log, name string, next mcpserver.Handler, onError func(error)) mcpserver.Handler {
	return func(ctx context.Context, args map[string]any) (result *mcp.CallToolResult, err error) {
		completed := false
		metadata := auditMetadata(args)
		defer func() {
			success := completed && err == nil && result != nil && !result.IsError
			if result != nil {
				if value, ok := result.StructuredContent.(map[string]any); ok {
					if code, ok := value["exit_code"]; ok {
						switch code := code.(type) {
						case int:
							metadata["exit_code"] = code
							success = success && code == 0
						case float64:
							metadata["exit_code"] = code
							success = success && code == 0
						default:
							success = false
						}
					}
					if name == "system_inspect" && args["action"] == "diagnostics" && value["healthy"] == false {
						success = false
					}
				}
			}
			record := map[string]any{"timestamp": time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00"), "tool": name, "success": success, "metadata": metadata}
			if !completed || err != nil {
				record["error"] = "ToolError"
			}
			if auditErr := log.Append(record); auditErr != nil && onError != nil {
				onError(auditErr)
			}
		}()
		result, err = next(ctx, args)
		completed = true
		return result, err
	}
}
