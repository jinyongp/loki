package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"loki/internal/devtools"
)

type recordingRuntimeClient struct {
	request any
	result  json.RawMessage
}

func (c *recordingRuntimeClient) Call(_ context.Context, request any) (json.RawMessage, error) {
	c.request = request
	return c.result, nil
}

func TestDevtoolsMCPMatchesApprovedCatalog(t *testing.T) {
	definitions, handlers, err := DevtoolsMCP(&recordingRuntimeClient{})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != len(devtools.ApprovedNames()) || len(handlers) != len(definitions) {
		t.Fatalf("definitions=%d handlers=%d approved=%d", len(definitions), len(handlers), len(devtools.ApprovedNames()))
	}
	seen := map[string]bool{}
	for _, definition := range definitions {
		if definition.Name == "" || handlers[definition.Name] == nil || seen[definition.Name] {
			t.Fatalf("invalid generated definition %q", definition.Name)
		}
		seen[definition.Name] = true
	}
	if seen["run"] || seen["secret_set"] || seen["process_logs"] {
		t.Fatal("excluded devtools command was published")
	}
	if !seen["doctor"] || !seen["task_add"] || !seen["process_start"] {
		t.Fatal("approved devtools command was not published")
	}
}

func TestDevtoolsMCPForwardsSecretMetadataOutsideCLIInput(t *testing.T) {
	client := &recordingRuntimeClient{result: json.RawMessage(`{"id":"execution"}`)}
	definitions, handlers, err := DevtoolsMCP(client)
	if err != nil {
		t.Fatal(err)
	}
	var processSchema map[string]any
	for _, definition := range definitions {
		if definition.Name == "process_start" {
			processSchema = definition.InputSchema.(map[string]any)
		}
	}
	properties := processSchema["properties"].(map[string]any)
	if properties[devtoolsSecretProfile] == nil || properties[devtoolsSecretNames] == nil {
		t.Fatal("process secret metadata is absent from the MCP schema")
	}
	result, err := handlers["process_start"](context.Background(), map[string]any{
		"args": []any{"web"}, "request-id": "00000000-0000-0000-0000-000000000000",
		devtoolsSecretProfile: "local", devtoolsSecretNames: []any{"API_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StructuredContent.(map[string]any)["id"] != "execution" {
		t.Fatalf("result = %#v", result)
	}
	request := client.request.(devtoolsRuntimeRequest)
	if request.Operation != "devtools_call" || request.Command != "process start" || request.Profile != "local" || len(request.Secrets) != 1 || request.Secrets[0] != "API_TOKEN" {
		t.Fatalf("runtime request = %#v", request)
	}
	if strings.Contains(string(request.Input), "secret_profile") || strings.Contains(string(request.Input), "secret_names") {
		t.Fatalf("Loki metadata leaked into devtools input: %s", request.Input)
	}
}
