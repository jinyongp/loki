package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/audit"
	"loki/internal/fault"
	"loki/internal/mcpserver"
)

var mcpInvocationCounter atomic.Uint64

// Metadata is an explicit allowlist. Never serialize the entire tool request
// or result: both may contain file contents, typed credentials or response bodies.
func auditMetadata(args map[string]any) map[string]any {
	metadata := map[string]any{}
	budget := 8192
	for _, key := range []string{"action", "operation", "cwd", "path", "source", "destination", "profile", "action_name", "name", "scope", "session_id", "share_id", "kind", "request_id", "port", "full_page", "ttl_seconds", "overwrite", "index", "staged", "reverse", "regex", "target", "issue", "field_id"} {
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

func auditInvocationID(now time.Time) string {
	return fmt.Sprintf("%016x-%016x", uint64(now.UnixNano()), mcpInvocationCounter.Add(1))
}

func auditSessionRef(ctx context.Context) string {
	id, ok := mcpserver.SessionID(ctx)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:8])
}

func appendAudit(log *audit.Log, record map[string]any, onError func(error)) {
	if auditErr := log.Append(record); auditErr != nil && onError != nil {
		onError(auditErr)
	}
}

func auditHandler(log *audit.Log, name string, next mcpserver.Handler, onError func(error)) mcpserver.Handler {
	return func(ctx context.Context, args map[string]any) (result *mcp.CallToolResult, err error) {
		started := time.Now().UTC()
		invocationID := auditInvocationID(started)
		sessionRef := auditSessionRef(ctx)
		metadata := auditMetadata(args)
		startRecord := map[string]any{
			"timestamp":     started.Format("2006-01-02T15:04:05.000000+00:00"),
			"phase":         "start",
			"invocation_id": invocationID,
			"tool":          name,
			"metadata":      metadata,
		}
		if sessionRef != "" {
			startRecord["session_ref"] = sessionRef
		}
		appendAudit(log, startRecord, onError)

		completed := false
		defer func() {
			recovered := recover()
			finished := time.Now().UTC()
			success := completed && recovered == nil && err == nil && result != nil && !result.IsError
			outcome := "success"
			switch {
			case recovered != nil:
				success = false
				outcome = "panic"
			case !completed || err != nil || result == nil:
				success = false
				outcome = "handler_error"
			case result.IsError:
				success = false
				outcome = "tool_error"
			}
			if result != nil {
				if value, ok := result.StructuredContent.(map[string]any); ok {
					if code, ok := value["exit_code"]; ok {
						switch code := code.(type) {
						case int:
							metadata["exit_code"] = code
							if code != 0 {
								success = false
								outcome = "nonzero_exit"
							}
						case float64:
							metadata["exit_code"] = code
							if code != 0 {
								success = false
								outcome = "nonzero_exit"
							}
						default:
							success = false
							outcome = "tool_error"
						}
					}
					if name == "system_inspect" && args["action"] == "diagnostics" && value["healthy"] == false {
						success = false
						outcome = "tool_error"
					}
				}
			}
			record := map[string]any{
				"timestamp":     finished.Format("2006-01-02T15:04:05.000000+00:00"),
				"phase":         "terminal",
				"invocation_id": invocationID,
				"tool":          name,
				"success":       success,
				"outcome":       outcome,
				"duration_ms":   float64(finished.Sub(started).Microseconds()) / 1000,
				"metadata":      metadata,
			}
			if sessionRef != "" {
				record["session_ref"] = sessionRef
			}
			appendAudit(log, record, onError)
			if recovered != nil {
				panic(fault.WithCorrelation(
					fault.New(fault.CodeFailed, "unexpected server failure; run diagnostics and retry", false, "inspect system_inspect action=operation with this correlation_id before retrying"),
					invocationID,
				))
			}
		}()

		result, err = next(ctx, args)
		if result != nil {
			if result.Meta == nil {
				result.Meta = mcp.Meta{}
			}
			result.Meta["loki/correlation_id"] = invocationID
		}
		if err != nil {
			err = fault.WithCorrelation(err, invocationID)
		}
		completed = true
		return result, err
	}
}
