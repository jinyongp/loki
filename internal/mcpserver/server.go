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
// Snapshot metadata remains the compatibility artifact while migrated tool
// definitions are overlaid from Loki's Go-authored contract registry.
func New(handlers map[string]Handler) (*mcp.Server, error) {
	current, err := contract.Current()
	if err != nil {
		return nil, err
	}
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		return nil, err
	}
	return newServer(current, handlers, definitions, nil, contract.CurrentInstructions)
}

// NewConfigured derives widget origins from the active service configuration.
func NewConfigured(handlers map[string]Handler, origins ResourceOrigins) (*mcp.Server, error) {
	if err := origins.validate(); err != nil {
		return nil, err
	}
	current, err := contract.Current()
	if err != nil {
		return nil, err
	}
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		return nil, err
	}
	return newServer(current, handlers, definitions, &origins, contract.CurrentInstructions)
}

// NewConfiguredCurrent is retained as the product-facing constructor name.
func NewConfiguredCurrent(handlers map[string]Handler, origins ResourceOrigins) (*mcp.Server, error) {
	return NewConfigured(handlers, origins)
}

// NewConfiguredAvailable exposes only the contract definitions that have
// active handlers. It rejects unknown handler names and preserves contract
// ordering for the enabled subset.
func NewConfiguredAvailable(handlers map[string]Handler, origins ResourceOrigins) (*mcp.Server, error) {
	if err := origins.validate(); err != nil {
		return nil, err
	}
	current, err := contract.Current()
	if err != nil {
		return nil, err
	}
	definitions, err := contract.CurrentDefinitions()
	if err != nil {
		return nil, err
	}
	selected := make([]*mcp.Tool, 0, len(handlers))
	known := make(map[string]bool, len(definitions))
	for _, definition := range definitions {
		known[definition.Name] = true
		if handlers[definition.Name] != nil {
			selected = append(selected, definition)
		}
	}
	for name, handler := range handlers {
		if handler == nil {
			return nil, fmt.Errorf("nil implementation for %s", name)
		}
		if !known[name] {
			return nil, fmt.Errorf("unknown tool implementation %s", name)
		}
	}
	return newServer(current, handlers, selected, &origins, contract.CurrentInstructions)
}

func newServer(snapshot *contract.Snapshot, handlers map[string]Handler, definitions []*mcp.Tool, origins *ResourceOrigins, instructions string) (*mcp.Server, error) {
	if len(handlers) != len(definitions) {
		return nil, fmt.Errorf("need %d handlers, got %d", len(definitions), len(handlers))
	}
	var init mcp.InitializeResult
	if err := json.Unmarshal(snapshot.Initialize, &init); err != nil {
		return nil, err
	}
	if instructions != "" {
		init.Instructions = instructions
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
	for _, raw := range snapshot.Resources {
		var resource mcp.Resource
		if err := json.Unmarshal(raw, &resource); err != nil {
			return nil, err
		}
		if origins != nil {
			resource.Meta = origins.metadata(resource.URI)
		}
		contents, ok := snapshot.ResourceContents[resource.URI]
		if !ok {
			return nil, fmt.Errorf("missing resource %s", resource.URI)
		}
		server.AddResource(&resource, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			var result mcp.ReadResourceResult
			err := json.Unmarshal(contents, &result)
			if err == nil && origins != nil {
				for _, content := range result.Contents {
					content.Meta = origins.metadata(resource.URI)
				}
			}
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
			if recovered := recover(); recovered != nil {
				recoveredErr, ok := recovered.(error)
				if !ok {
					recoveredErr = errors.New("panic")
				}
				result = errorResult(fault.Describe(recoveredErr))
				err = nil
			}
		}()
		var input map[string]any
		args := req.Params.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if json.Unmarshal(args, &input) != nil || input == nil {
			return errorResult(fault.Describe(fault.New(fault.CodeInvalidInput, "invalid arguments: request; inspect the tool schema and retry", false, "inspect the tool schema and correct the request"))), nil
		}
		for key := range input {
			if _, known := schema.Properties[key]; !known {
				return errorResult(fault.Describe(fault.New(fault.CodeInvalidInput, "invalid arguments: unknown field; inspect the tool schema and retry", false, "remove unknown fields and retry"))), nil
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
			return errorResult(fault.Describe(fault.New(fault.CodeInvalidInput, "invalid arguments: "+name+"; inspect the tool schema and retry", false, "correct the invalid fields and retry"))), nil
		}
		if err := resolved.ApplyDefaults(&input); err != nil {
			return nil, errors.New("invalid server defaults")
		}
		if req.Session != nil {
			ctx = withSessionID(ctx, req.Session.ID())
		}
		result, err = handler(ctx, input)
		if err != nil {
			return errorResult(fault.Describe(err)), nil
		}
		if result == nil {
			return errorResult(fault.Describe(errors.New("nil tool result"))), nil
		}
		return result, nil
	}, nil
}

func errorResult(detail fault.Detail) *mcp.CallToolResult {
	public := map[string]any{
		"code":      string(detail.Code),
		"message":   detail.Message,
		"retryable": detail.Retryable,
	}
	meta := mcp.Meta{"loki/error": public}
	if detail.CorrelationID != "" {
		public["correlation_id"] = detail.CorrelationID
		meta["loki/correlation_id"] = detail.CorrelationID
	}
	if detail.NextAction != "" {
		public["next_action"] = detail.NextAction
	}
	return &mcp.CallToolResult{
		Meta:    meta,
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: detail.Message}},
	}
}

// Object returns matching text and structured JSON payloads.
func Object(value map[string]any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &mcp.CallToolResult{StructuredContent: value, Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil
}
