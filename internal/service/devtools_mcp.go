package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/devtools"
	"loki/internal/mcpserver"
)

const (
	devtoolsSecretProfile = "secret_profile"
	devtoolsSecretNames   = "secret_names"
)

type devtoolsRuntimeRequest struct {
	Operation string `json:"operation"`
	devtoolsRequest
}

// DevtoolsMCP builds the public tool definitions and forwarding handlers from
// the pinned, reviewed catalog. Tool names are the CLI path joined by underscores.
func DevtoolsMCP(client RuntimeCaller) ([]*mcp.Tool, map[string]mcpserver.Handler, error) {
	commands, err := devtools.EmbeddedCatalog()
	if err != nil {
		return nil, nil, err
	}
	definitions := make([]*mcp.Tool, 0, len(commands))
	handlers := make(map[string]mcpserver.Handler, len(commands))
	for _, command := range commands {
		name := strings.ReplaceAll(command.Name, " ", "_")
		if handlers[name] != nil {
			return nil, nil, errors.New("duplicate generated devtools tool")
		}
		var input map[string]any
		if json.Unmarshal(command.InputSchema, &input) != nil || input == nil {
			return nil, nil, errors.New("invalid pinned devtools input schema")
		}
		if command.Name == "process start" || command.Name == "process restart" {
			properties, ok := input["properties"].(map[string]any)
			if !ok {
				return nil, nil, errors.New("devtools process schema has no properties")
			}
			properties[devtoolsSecretProfile] = map[string]any{
				"type": "string", "minLength": 1, "maxLength": 128,
				"pattern":     "^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$",
				"description": "Loki encrypted-vault profile used only for selected process secrets.",
			}
			properties[devtoolsSecretNames] = map[string]any{
				"type": "array", "minItems": 1, "maxItems": 256, "uniqueItems": true,
				"items":       map[string]any{"type": "string", "pattern": "^[A-Za-z_][A-Za-z0-9_]*$"},
				"description": "Encrypted Loki secret names injected into the configured process.",
			}
			input["dependentRequired"] = map[string]any{
				devtoolsSecretProfile: []string{devtoolsSecretNames},
				devtoolsSecretNames:   []string{devtoolsSecretProfile},
			}
		}
		definitions = append(definitions, &mcp.Tool{
			Name: name, Description: command.Description,
			InputSchema: input, OutputSchema: command.OutputSchema,
		})
		commandName := command.Name
		handlers[name] = func(ctx context.Context, arguments map[string]any) (*mcp.CallToolResult, error) {
			input := make(map[string]any, len(arguments))
			for key, value := range arguments {
				input[key] = value
			}
			profile, _ := input[devtoolsSecretProfile].(string)
			delete(input, devtoolsSecretProfile)
			var secretNames []string
			if rawNames, ok := input[devtoolsSecretNames].([]any); ok {
				for _, rawName := range rawNames {
					name, ok := rawName.(string)
					if !ok {
						return nil, errors.New("invalid devtools secret names")
					}
					secretNames = append(secretNames, name)
				}
			}
			delete(input, devtoolsSecretNames)
			encoded, err := json.Marshal(input)
			if err != nil {
				return nil, err
			}
			response, err := client.Call(ctx, devtoolsRuntimeRequest{
				Operation: "devtools_call",
				devtoolsRequest: devtoolsRequest{
					Command: commandName, Input: encoded, Profile: profile, Secrets: secretNames,
				},
			})
			if err != nil {
				return nil, err
			}
			decoder := json.NewDecoder(bytes.NewReader(response))
			decoder.UseNumber()
			var structured any
			if decoder.Decode(&structured) != nil || structured == nil || decoder.Decode(&struct{}{}) == nil {
				return nil, errors.New("invalid devtools runtime response")
			}
			return &mcp.CallToolResult{
				StructuredContent: structured,
				Content:           []mcp.Content{&mcp.TextContent{Text: string(response)}},
			}, nil
		}
	}
	return definitions, handlers, nil
}
