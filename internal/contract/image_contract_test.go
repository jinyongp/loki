package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestImageContractsAreClosedAndReplayAware(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	read := seenDefinition(definitions, "read_image")
	share := seenDefinition(definitions, "share_image")
	write := seenDefinition(definitions, "write_image")
	if read == nil || share == nil || write == nil {
		t.Fatalf("image contracts missing: read=%#v share=%#v write=%#v", read, share, write)
	}
	for _, tool := range []*mcp.Tool{read, share, write} {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var input map[string]any
		if err := json.Unmarshal(encoded, &input); err != nil {
			t.Fatal(err)
		}
		if input["additionalProperties"] != false {
			t.Errorf("%s input remains open: %#v", tool.Name, input)
		}
		for name, raw := range input["properties"].(map[string]any) {
			property := raw.(map[string]any)
			if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
				t.Errorf("%s property %s has no description", tool.Name, name)
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
			t.Errorf("%s output remains open: %#v", tool.Name, output)
		}
	}

	hint := func(value *bool) bool { return value != nil && *value }
	if read.Annotations == nil || !read.Annotations.ReadOnlyHint || !read.Annotations.IdempotentHint ||
		hint(read.Annotations.DestructiveHint) || hint(read.Annotations.OpenWorldHint) {
		t.Fatalf("read_image annotations = %#v", read.Annotations)
	}
	if share.Annotations == nil || share.Annotations.ReadOnlyHint || hint(share.Annotations.DestructiveHint) ||
		!share.Annotations.IdempotentHint || !hint(share.Annotations.OpenWorldHint) {
		t.Fatalf("share_image annotations = %#v", share.Annotations)
	}
	if write.Annotations == nil || write.Annotations.ReadOnlyHint || !hint(write.Annotations.DestructiveHint) ||
		write.Annotations.IdempotentHint || hint(write.Annotations.OpenWorldHint) {
		t.Fatalf("write_image annotations = %#v", write.Annotations)
	}

	encoded, err := json.Marshal(write.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var writeInput map[string]any
	if err := json.Unmarshal(encoded, &writeInput); err != nil {
		t.Fatal(err)
	}
	branches := writeInput["oneOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("write_image branches = %#v", branches)
	}
	foundCreate, foundOverwrite := false, false
	for _, raw := range branches {
		branch := raw.(map[string]any)
		properties := branch["properties"].(map[string]any)
		overwrite := properties["overwrite"].(map[string]any)
		required := map[string]bool{}
		for _, item := range branch["required"].([]any) {
			required[item.(string)] = true
		}
		switch overwrite["const"] {
		case false:
			foundCreate = true
			if _, exists := properties["expected_sha256"]; exists {
				t.Fatal("create-only write_image accepts expected_sha256")
			}
		case true:
			foundOverwrite = true
			if !required["expected_sha256"] {
				t.Fatal("overwrite write_image does not require expected_sha256")
			}
		default:
			t.Fatalf("write_image overwrite branch = %#v", overwrite)
		}
	}
	if !foundCreate || !foundOverwrite {
		t.Fatalf("write_image modes missing: create=%v overwrite=%v", foundCreate, foundOverwrite)
	}

	encoded, err = json.Marshal(share.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var shareInput map[string]any
	if err := json.Unmarshal(encoded, &shareInput); err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{}
	for _, raw := range shareInput["required"].([]any) {
		required[raw.(string)] = true
	}
	if !required["path"] || !required["request_id"] {
		t.Fatalf("share_image required = %#v", required)
	}
	encoded, err = json.Marshal(share.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var shareOutput map[string]any
	if err := json.Unmarshal(encoded, &shareOutput); err != nil {
		t.Fatal(err)
	}
	if _, exists := shareOutput["properties"].(map[string]any)["share_id"]; !exists {
		t.Fatal("share_image output omits share_id")
	}

	shareOps := share.Meta["loki/operations"].(map[string]any)
	shareSemantics := shareOps["share"].(map[string]any)
	if shareSemantics["replay"] != string(ReplayRequestID) || shareSemantics["request_id_field"] != "request_id" {
		t.Fatalf("share_image operation metadata = %#v", shareSemantics)
	}
	writeOps := write.Meta["loki/operations"].(map[string]any)
	if writeOps["write"].(map[string]any)["replay"] != string(ReplayUnsafe) {
		t.Fatalf("write_image operation metadata = %#v", writeOps)
	}
}
