package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/fault"
	"loki/internal/policy"
	"loki/internal/process"
)

type RuntimeCaller interface {
	Call(context.Context, any) (json.RawMessage, error)
}

func toolEnvironment(overrides map[string]string) []string {
	values := map[string]string{}
	for _, entry := range process.Environment() {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	for name, value := range map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:18766", "HTTP_PROXY": "http://127.0.0.1:18766",
		"https_proxy": "http://127.0.0.1:18766", "http_proxy": "http://127.0.0.1:18766",
		"NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost",
		"GIT_CONFIG_GLOBAL": "/etc/loki-go/gitconfig", "SSH_AUTH_SOCK": "/run/loki-go/signing/agent.sock",
		"PATH": "/opt/loki/toolchain/bin:/opt/loki/bin:/usr/local/bin:/usr/bin:/bin",
	} {
		values[name] = value
	}
	for name, value := range overrides {
		values[name] = value
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, 0, len(names))
	for _, name := range names {
		result = append(result, name+"="+values[name])
	}
	return result
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
