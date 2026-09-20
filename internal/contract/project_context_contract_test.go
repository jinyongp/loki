package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProjectContextContractsCloseNestedStructures(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	read := seenDefinition(definitions, "project_context")
	write := seenDefinition(definitions, "project_context_write")
	if read == nil || write == nil {
		t.Fatalf("project context contracts missing: read=%#v write=%#v", read, write)
	}

	for _, tool := range []*mcp.Tool{read, write} {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(encoded, &input); err != nil {
			t.Fatal(err)
		}
		if input["additionalProperties"] != false {
			t.Fatalf("%s input remains open: %#v", tool.Name, input)
		}
		for name, raw := range input["properties"].(map[string]any) {
			property := raw.(map[string]any)
			if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
				t.Errorf("%s property %s lacks description", tool.Name, name)
			}
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
			t.Fatalf("%s output remains open: %#v", tool.Name, output)
		}
	}

	encoded, err := json.Marshal(read.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var readOutput map[string]any
	if err := json.Unmarshal(encoded, &readOutput); err != nil {
		t.Fatal(err)
	}
	readProperties := readOutput["properties"].(map[string]any)
	for _, name := range []string{"basis", "guidance", "skills", "coordination", "transition", "checkpoint"} {
		schema := readProperties[name].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Errorf("project_context nested %s remains open: %#v", name, schema)
		}
	}
	coordination := readProperties["coordination"].(map[string]any)
	current := coordination["properties"].(map[string]any)["current"].(map[string]any)
	if current["additionalProperties"] != false {
		t.Fatalf("project_context coordination projection wrapper remains open: %#v", current)
	}
	checkpoint := readProperties["checkpoint"].(map[string]any)
	record := checkpoint["properties"].(map[string]any)["record"].(map[string]any)
	if record["additionalProperties"] != false {
		t.Fatalf("project_context checkpoint record remains open: %#v", record)
	}

	hint := func(value *bool) bool { return value != nil && *value }
	if read.Annotations == nil || !read.Annotations.ReadOnlyHint || !read.Annotations.IdempotentHint ||
		hint(read.Annotations.DestructiveHint) || hint(read.Annotations.OpenWorldHint) {
		t.Fatalf("project_context annotations = %#v", read.Annotations)
	}
	if write.Annotations == nil || write.Annotations.ReadOnlyHint || hint(write.Annotations.DestructiveHint) ||
		!write.Annotations.IdempotentHint || hint(write.Annotations.OpenWorldHint) {
		t.Fatalf("project_context_write annotations = %#v", write.Annotations)
	}

	encoded, err = json.Marshal(write.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var writeOutput map[string]any
	if err := json.Unmarshal(encoded, &writeOutput); err != nil {
		t.Fatal(err)
	}
	writeRecord := writeOutput["properties"].(map[string]any)["record"].(map[string]any)
	if writeRecord["additionalProperties"] != false {
		t.Fatalf("project_context_write record remains open: %#v", writeRecord)
	}
	operations, ok := write.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 1 {
		t.Fatalf("project_context_write operation metadata = %#v", write.Meta)
	}
	semantics := operations["checkpoint"].(map[string]any)
	if semantics["replay"] != string(ReplayRequestID) ||
		semantics["request_id_field"] != "request_id" ||
		semantics["failure_atomicity"] != string(FailureSingleResource) ||
		semantics["crash_recovery"] != string(CrashRecoveryJournaled) {
		t.Fatalf("project_context_write semantics = %#v", semantics)
	}
	guards := semantics["concurrency_fields"].([]string)
	if len(guards) != 2 || guards[0] != "expected_basis" || guards[1] != "expected_previous" {
		t.Fatalf("project_context_write concurrency guards = %#v", guards)
	}
}
