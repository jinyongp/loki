package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestActionInputContractRequiresDescribedFields(t *testing.T) {
	_, err := (ActionInputContract{
		Title:             "fixture",
		ActionDescription: "Fixture action.",
		Fields: []ActionField{{
			Name:   "path",
			Schema: map[string]any{"type": "string"},
		}},
		Variants: []ActionVariant{{Name: "read", Required: []string{"path"}}},
	}).Schema()
	if err == nil || !strings.Contains(err.Error(), "requires a description") {
		t.Fatalf("missing field description error = %v", err)
	}

	_, err = (ActionInputContract{
		Title:             "fixture",
		ActionDescription: "Fixture action.",
		Fields: []ActionField{{
			Name:   "path",
			Schema: map[string]any{"type": "string", "description": "Path."},
		}},
		Variants: []ActionVariant{{Name: "read", Required: []string{"missing"}}},
	}).Schema()
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown action field error = %v", err)
	}
}

func TestActionInputContractSupportsDefaultAction(t *testing.T) {
	schema, err := (ActionInputContract{
		Title:             "fixture",
		ActionDescription: "Fixture action.",
		DefaultAction:     "read",
		Fields: []ActionField{{
			Name: "path", Schema: map[string]any{"type": "string", "description": "Path."},
		}},
		Variants: []ActionVariant{
			{Name: "read"},
			{Name: "write", Required: []string{"path"}},
		},
	}).Schema()
	if err != nil {
		t.Fatal(err)
	}
	if _, required := schema["required"]; required {
		t.Fatalf("defaulted root unexpectedly requires action: %#v", schema)
	}
	properties := schema["properties"].(map[string]any)
	action := properties["action"].(map[string]any)
	if action["default"] != "read" {
		t.Fatalf("action default = %#v", action)
	}
	branches := schema["oneOf"].([]any)
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		name := branchProperties["action"].(map[string]any)["const"].(string)
		required := map[string]bool{}
		for _, item := range branch["required"].([]string) {
			required[item] = true
		}
		if name == "read" && required["action"] {
			t.Fatal("default action branch requires action")
		}
		if name == "write" && (!required["action"] || !required["path"]) {
			t.Fatalf("write required = %#v", required)
		}
	}

	_, err = (ActionInputContract{
		Title:             "fixture",
		ActionDescription: "Fixture action.",
		DefaultAction:     "missing",
		Variants:          []ActionVariant{{Name: "read"}},
	}).Schema()
	if err == nil || !strings.Contains(err.Error(), "default action") {
		t.Fatalf("invalid default action error = %v", err)
	}
}

func TestSystemInspectUsesGeneratedActionContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "system_inspect")
	if tool == nil {
		t.Fatal("system_inspect definition missing")
	}
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	if properties["action"].(map[string]any)["default"] != "server" {
		t.Fatalf("system_inspect action = %#v", properties["action"])
	}
	for _, name := range []string{"action", "port", "limit", "correlation_id"} {
		property := properties[name].(map[string]any)
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("system_inspect property %s has no description", name)
		}
	}
	branches := schema["oneOf"].([]any)
	if len(branches) != 6 {
		t.Fatalf("system_inspect branches = %#v", branches)
	}
	foundOperation := false
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		if action != "operation" {
			continue
		}
		foundOperation = true
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		if !required["action"] || !required["correlation_id"] {
			t.Fatalf("operation required = %#v", required)
		}
		if _, exists := branchProperties["limit"]; exists {
			t.Fatal("operation accepts irrelevant activity limit")
		}
	}
	if !foundOperation {
		t.Fatal("system_inspect operation branch missing")
	}
}

func TestWorkspaceReadUsesGeneratedActionContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "workspace_read")
	if tool == nil {
		t.Fatal("workspace_read definition missing")
	}
	if !strings.Contains(tool.Description, "has_more") || !strings.Contains(tool.Description, "eof") || !strings.Contains(tool.Description, "truncated") {
		t.Fatalf("workspace_read description = %q", tool.Description)
	}
	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	properties := input["properties"].(map[string]any)
	for name, raw := range properties {
		property := raw.(map[string]any)
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("workspace_read property %s has no description", name)
		}
	}
	branches := input["oneOf"].([]any)
	if len(branches) != 5 {
		t.Fatalf("workspace_read branches = %#v", branches)
	}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		switch action {
		case "list":
			if _, exists := branchProperties["query"]; exists {
				t.Fatal("list accepts search query")
			}
		case "file", "revisions":
			if !required["path"] {
				t.Fatalf("%s does not require path", action)
			}
		case "search":
			if !required["query"] {
				t.Fatal("search does not require query")
			}
		case "revision_diff":
			if !required["path"] || !required["revision"] {
				t.Fatalf("revision_diff required = %#v", required)
			}
		}
	}
	outputEncoded, err := json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(outputEncoded, &output); err != nil {
		t.Fatal(err)
	}
	outputBranches := output["oneOf"].([]any)
	if len(outputBranches) != 6 {
		t.Fatalf("workspace_read output branches = %#v", outputBranches)
	}
	for _, raw := range outputBranches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("workspace_read output branch is open: %#v", branch)
		}
	}
}

func TestWorkspaceEditUsesGeneratedActionContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "workspace_edit")
	if tool == nil {
		t.Fatal("workspace_edit definition missing")
	}
	if !strings.Contains(tool.Description, "multiple files") || !strings.Contains(tool.Description, "patch-file limit") {
		t.Fatalf("workspace_edit description = %q", tool.Description)
	}

	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("workspace_edit properties = %#v", schema["properties"])
	}
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("workspace_edit property %s = %#v", name, raw)
		}
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("workspace_edit property %s has no description", name)
		}
	}
	branches, ok := schema["oneOf"].([]any)
	if !ok || len(branches) != 4 {
		t.Fatalf("workspace_edit oneOf = %#v", schema["oneOf"])
	}

	var replace map[string]any
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)
		if action["const"] == "replace" {
			replace = branch
			break
		}
	}
	if replace == nil {
		t.Fatal("replace action branch missing")
	}
	required := map[string]bool{}
	for _, raw := range replace["required"].([]any) {
		required[raw.(string)] = true
	}
	for _, name := range []string{"action", "path", "old", "new", "expected_sha256", "expected_replacements"} {
		if !required[name] {
			t.Errorf("replace action does not require %s", name)
		}
	}
}

func TestWorkspaceRecoveryToolsRequireCASAndClosedResults(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"restore_workspace_file", "remove_tracked_file"} {
		tool := seenDefinition(definitions, name)
		if tool == nil {
			t.Fatalf("%s definition missing", name)
		}
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(encoded, &input); err != nil {
			t.Fatal(err)
		}
		properties := input["properties"].(map[string]any)
		for propertyName, raw := range properties {
			property := raw.(map[string]any)
			if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
				t.Errorf("%s property %s has no description", name, propertyName)
			}
		}
		required := map[string]bool{}
		for _, raw := range input["required"].([]any) {
			required[raw.(string)] = true
		}
		if !required["path"] || !required["expected_sha256"] {
			t.Fatalf("%s required = %#v", name, required)
		}
		if name == "restore_workspace_file" && !required["revision"] {
			t.Fatalf("%s does not require revision", name)
		}

		outputEncoded, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var output map[string]any
		if err := json.Unmarshal(outputEncoded, &output); err != nil {
			t.Fatal(err)
		}
		if output["additionalProperties"] != false {
			t.Fatalf("%s output remains open: %#v", name, output)
		}
		operations, ok := tool.Meta["loki/operations"].(map[string]any)
		if !ok || len(operations) != 1 {
			t.Fatalf("%s operation metadata = %#v", name, tool.Meta)
		}
		operationName := "restore"
		if name == "remove_tracked_file" {
			operationName = "remove"
		}
		semantics := operations[operationName].(map[string]any)
		if semantics["replay"] != string(ReplayGuarded) ||
			semantics["failure_atomicity"] != string(FailureSingleResource) ||
			semantics["crash_recovery"] != string(CrashRecoveryInspect) {
			t.Fatalf("%s semantics = %#v", name, semantics)
		}
		guards := semantics["concurrency_fields"].([]string)
		if len(guards) != 1 || guards[0] != "expected_sha256" {
			t.Fatalf("%s guards = %#v", name, guards)
		}
	}
}

func TestGitStageUsesGeneratedActionContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "git_stage")
	if tool == nil {
		t.Fatal("git_stage definition missing")
	}
	if !strings.Contains(tool.Description, "multiple paths") ||
		!strings.Contains(tool.Description, "multi-file") ||
		!strings.Contains(tool.Description, "git_inspect action=index") {
		t.Fatalf("git_stage description = %q", tool.Description)
	}

	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	for name, raw := range properties {
		property := raw.(map[string]any)
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Errorf("git_stage property %s has no description", name)
		}
	}
	branches := schema["oneOf"].([]any)
	if len(branches) != 3 {
		t.Fatalf("git_stage oneOf = %#v", branches)
	}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		required := map[string]bool{}
		for _, rawRequired := range branch["required"].([]any) {
			required[rawRequired.(string)] = true
		}
		if !required["expected_index_sha256"] {
			t.Errorf("%s does not require expected_index_sha256", action)
		}
	}
}

func TestGitHubIssueFieldsToolsSeparateReadAndWriteAuthority(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	read := seenDefinition(definitions, "github_issue_fields_read")
	write := seenDefinition(definitions, "github_issue_fields_write")
	if read == nil || write == nil {
		t.Fatalf("Issue Fields split definitions missing: read=%#v write=%#v", read, write)
	}
	if seenDefinition(definitions, "github_issue_fields") != nil {
		t.Fatal("retired mixed GitHub Issue Fields tool remains public")
	}
	if read.Annotations == nil || !read.Annotations.ReadOnlyHint || read.Annotations.DestructiveHint == nil || *read.Annotations.DestructiveHint {
		t.Fatalf("read annotations = %#v", read.Annotations)
	}
	if write.Annotations == nil || write.Annotations.ReadOnlyHint || write.Annotations.DestructiveHint == nil || !*write.Annotations.DestructiveHint {
		t.Fatalf("write annotations = %#v", write.Annotations)
	}
	for _, tool := range []*mcp.Tool{read, write} {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema map[string]any
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		properties := schema["properties"].(map[string]any)
		for name, raw := range properties {
			property := raw.(map[string]any)
			if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
				t.Errorf("%s property %s has no description", tool.Name, name)
			}
		}
	}
	if read.OutputSchema == nil || write.OutputSchema == nil {
		t.Fatal("split GitHub Issue Fields tools require output schemas")
	}
}
