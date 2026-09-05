package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loki/internal/buildinfo"
	"loki/internal/contract"
	"loki/internal/fault"
)

type Handler func(context.Context, map[string]any) (*mcp.CallToolResult, error)

// New refuses incomplete or misspelled registrations before accepting requests.
// The captured catalog is the single source for public schemas and metadata.
func New(handlers map[string]Handler) (*mcp.Server, error) {
	baseline, err := contract.Baseline()
	if err != nil {
		return nil, err
	}
	definitions, err := baseline.Definitions()
	if err != nil {
		return nil, err
	}
	if len(handlers) != len(definitions) {
		return nil, fmt.Errorf("need %d handlers, got %d", len(definitions), len(handlers))
	}
	var init mcp.InitializeResult
	if err = json.Unmarshal(baseline.Initialize, &init); err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "loki",
		Version: buildinfo.Version,
	}, &mcp.ServerOptions{Instructions: init.Instructions, Capabilities: init.Capabilities, PageSize: 100})
	for _, definition := range definitions {
		handler := handlers[definition.Name]
		if handler == nil {
			return nil, fmt.Errorf("missing implementation for %s", definition.Name)
		}
		wrapped, err := wrap(definition, handler)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", definition.Name, err)
		}
		server.AddTool(definition, wrapped)
	}
	for _, raw := range baseline.Resources {
		var resource mcp.Resource
		if err = json.Unmarshal(raw, &resource); err != nil {
			return nil, err
		}
		contents, ok := baseline.ResourceContents[resource.URI]
		if !ok {
			return nil, fmt.Errorf("missing resource %s", resource.URI)
		}
		server.AddResource(&resource, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			var result mcp.ReadResourceResult
			err := json.Unmarshal(contents, &result)
			return &result, err
		})
	}
	return server, nil
}

func wrap(tool *mcp.Tool, handler Handler) (mcp.ToolHandler, error) {
	data, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil, err
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal(data, &schema); err != nil {
		return nil, err
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, req *mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
		defer func() {
			if recover() != nil {
				result = errorResult("unexpected server failure; run diagnostics and retry")
				err = nil
			}
		}()
		var input map[string]any
		args := req.Params.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if json.Unmarshal(args, &input) != nil || input == nil {
			return errorResult("invalid arguments: request; inspect the tool schema and retry"), nil
		}
		// Pydantic's exposed callable models ignore extra arguments.
		for key := range input {
			if _, known := schema.Properties[key]; !known {
				delete(input, key)
			}
		}
		if err := resolved.Validate(input); err != nil {
			fields := []string{}
			for _, key := range schema.Required {
				if _, ok := input[key]; !ok {
					fields = append(fields, key)
				}
			}
			for key, value := range input {
				property, err := schema.Properties[key].Resolve(nil)
				if err != nil || property.Validate(value) != nil {
					fields = append(fields, key)
				}
			}
			sort.Strings(fields)
			name := strings.Join(fields, ", ")
			if name == "" {
				name = "request"
			}
			return errorResult("invalid arguments: " + name + "; inspect the tool schema and retry"), nil
		}
		if err := resolved.ApplyDefaults(&input); err != nil {
			return nil, errors.New("invalid server defaults")
		}
		result, err = handler(ctx, input)
		if err != nil {
			return errorResult(fault.Public(err)), nil
		}
		if result == nil {
			return errorResult("unexpected server failure; run diagnostics and retry"), nil
		}
		return result, nil
	}, nil
}

func errorResult(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

// Object returns the same text and structured JSON pair as Python dict tools.
func Object(value map[string]any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{StructuredContent: value, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
}
