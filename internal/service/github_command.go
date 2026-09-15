package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/githubapp"
	"loki/internal/mcpserver"
	"loki/internal/process"
	"loki/internal/rpc"
)

type GitHubCommandRunner interface {
	Run(context.Context, githubapp.CommandRequest) (process.Result, error)
}

type githubCommandRequest struct {
	Operation string   `json:"operation"`
	Target    string   `json:"target"`
	Args      []string `json:"args"`
	Input     string   `json:"input"`
}

func GitHubCommandOperations(runner GitHubCommandRunner) map[string]rpc.Operation {
	return map[string]rpc.Operation{"github_command": {
		Permission: rpc.Agent,
		Handle: func(ctx context.Context, raw json.RawMessage) (any, error) {
			var request githubCommandRequest
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&request) != nil {
				return nil, errors.New("invalid GitHub command arguments")
			}
			var trailing any
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				return nil, errors.New("invalid GitHub command arguments")
			}
			if request.Operation != "github_command" || runner == nil {
				return nil, errors.New("GitHub command is not configured")
			}
			result, err := runner.Run(ctx, githubapp.CommandRequest{
				Target: request.Target, Args: request.Args, Input: []byte(request.Input),
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"exit_code": result.ExitCode, "output": result.Output, "truncated": result.Truncated,
				"timed_out": result.TimedOut, "canceled": result.Canceled,
			}, nil
		},
	}}
}

type githubCommandMCPRequest struct {
	Target string   `json:"target"`
	Args   []string `json:"args"`
	Input  string   `json:"input"`
}

func GitHubCommandHandlers(runtime RuntimeCaller) map[string]mcpserver.Handler {
	return map[string]mcpserver.Handler{"github": mcpserver.Typed(func(ctx context.Context, request githubCommandMCPRequest) (*mcp.CallToolResult, error) {
		return runtimeObject(ctx, runtime, map[string]any{
			"operation": "github_command", "target": request.Target,
			"args": request.Args, "input": request.Input,
		})
	})}
}
