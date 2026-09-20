package contract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"loki/internal/githubapp"
)

func TestGitHubEscapeHatchPublishesExplicitConservativeContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "github")
	if tool == nil {
		t.Fatal("github definition missing")
	}
	if !strings.Contains(tool.Description, "escape-hatch") || !strings.Contains(tool.Description, "not replay-safe") {
		t.Fatalf("github description = %q", tool.Description)
	}
	hint := func(value *bool) bool { return value != nil && *value }
	if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || !hint(tool.Annotations.DestructiveHint) ||
		tool.Annotations.IdempotentHint || !hint(tool.Annotations.OpenWorldHint) {
		t.Fatalf("github annotations = %#v", tool.Annotations)
	}

	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	if input["additionalProperties"] != false {
		t.Fatalf("github input remains open: %#v", input)
	}
	properties := input["properties"].(map[string]any)
	for _, name := range []string{"target", "command", "args", "input"} {
		property, ok := properties[name].(map[string]any)
		if !ok {
			t.Fatalf("github property %s missing", name)
		}
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("github property %s lacks description", name)
		}
	}
	required := map[string]bool{}
	for _, raw := range input["required"].([]any) {
		required[raw.(string)] = true
	}
	if !required["target"] || !required["command"] || required["args"] || required["input"] {
		t.Fatalf("github required = %#v", required)
	}

	capabilities := githubapp.RepositoryCommandCapabilities()
	commandEnum := []string{}
	for _, raw := range properties["command"].(map[string]any)["enum"].([]any) {
		commandEnum = append(commandEnum, raw.(string))
	}
	if !reflect.DeepEqual(commandEnum, capabilities.CommandGroups) {
		t.Fatalf("github command enum = %#v want %#v", commandEnum, capabilities.CommandGroups)
	}
	if maxItems := int(properties["args"].(map[string]any)["maxItems"].(float64)); maxItems != capabilities.MaxArguments-1 {
		t.Fatalf("github args maxItems = %d", maxItems)
	}

	encoded, err = json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	if output["additionalProperties"] != false {
		t.Fatalf("github output remains open: %#v", output)
	}

	meta, ok := tool.Meta["loki/github_command"].(map[string]any)
	if !ok {
		t.Fatalf("github capability metadata = %#v", tool.Meta)
	}
	if !reflect.DeepEqual(meta["command_groups"], capabilities.CommandGroups) ||
		!reflect.DeepEqual(meta["search_subcommands"], capabilities.SearchSubcommands) ||
		!reflect.DeepEqual(meta["prohibited_flags"], capabilities.ProhibitedFlags) ||
		meta["repository_token_only"] != true {
		t.Fatalf("github capability metadata = %#v", meta)
	}
	if meta["max_arguments"] != capabilities.MaxArguments ||
		meta["max_argument_bytes"] != capabilities.MaxArgumentBytes ||
		meta["max_argument_total"] != capabilities.MaxArgumentTotal ||
		meta["max_input_bytes"] != capabilities.MaxInputBytes {
		t.Fatalf("github capability bounds = %#v", meta)
	}

	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 1 {
		t.Fatalf("github operation metadata = %#v", tool.Meta)
	}
	semantics := operations["command"].(map[string]any)
	if semantics["replay"] != string(ReplayUnsafe) ||
		semantics["failure_atomicity"] != string(FailureUpstream) ||
		semantics["crash_recovery"] != string(CrashRecoveryUpstream) ||
		semantics["affected_resource_limit"] != 1 {
		t.Fatalf("github command semantics = %#v", semantics)
	}
}
