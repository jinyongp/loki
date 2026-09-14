package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/policy"
)

type RuntimeCaller interface {
	Call(context.Context, any) (json.RawMessage, error)
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
