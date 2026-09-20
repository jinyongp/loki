package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBrowserScreenshotContractsAreClosedAndReplayAware(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	screenshot := seenDefinition(definitions, "browser_screenshot")
	save := seenDefinition(definitions, "browser_save_screenshot")
	share := seenDefinition(definitions, "browser_share_screenshot")
	if screenshot == nil || save == nil || share == nil {
		t.Fatalf("browser screenshot contracts missing: screenshot=%#v save=%#v share=%#v", screenshot, save, share)
	}

	for _, tool := range []*mcp.Tool{screenshot, save, share} {
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
	if screenshot.Annotations == nil || !screenshot.Annotations.ReadOnlyHint || !screenshot.Annotations.IdempotentHint ||
		hint(screenshot.Annotations.DestructiveHint) || !hint(screenshot.Annotations.OpenWorldHint) {
		t.Fatalf("browser_screenshot annotations = %#v", screenshot.Annotations)
	}
	if save.Annotations == nil || save.Annotations.ReadOnlyHint || !hint(save.Annotations.DestructiveHint) || save.Annotations.IdempotentHint ||
		!hint(save.Annotations.OpenWorldHint) {
		t.Fatalf("browser_save_screenshot annotations = %#v", save.Annotations)
	}
	if share.Annotations == nil || share.Annotations.ReadOnlyHint || hint(share.Annotations.DestructiveHint) ||
		!share.Annotations.IdempotentHint || !hint(share.Annotations.OpenWorldHint) {
		t.Fatalf("browser_share_screenshot annotations = %#v", share.Annotations)
	}

	encoded, err := json.Marshal(save.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var saveInput map[string]any
	if err := json.Unmarshal(encoded, &saveInput); err != nil {
		t.Fatal(err)
	}
	branches := saveInput["oneOf"].([]any)
	if len(branches) != 2 {
		t.Fatalf("save screenshot branches = %#v", branches)
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
				t.Fatal("create-only screenshot save accepts expected_sha256")
			}
		case true:
			foundOverwrite = true
			if !required["expected_sha256"] {
				t.Fatal("overwrite screenshot save does not require expected_sha256")
			}
		default:
			t.Fatalf("save overwrite branch = %#v", overwrite)
		}
	}
	if !foundCreate || !foundOverwrite {
		t.Fatalf("save screenshot modes missing: create=%v overwrite=%v", foundCreate, foundOverwrite)
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
	if !required["request_id"] {
		t.Fatal("browser_share_screenshot does not require request_id")
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
		t.Fatal("browser_share_screenshot output omits share_id")
	}

	saveOps := save.Meta["loki/operations"].(map[string]any)
	if saveOps["save"].(map[string]any)["replay"] != string(ReplayUnsafe) {
		t.Fatalf("save operation metadata = %#v", saveOps)
	}
	shareOps := share.Meta["loki/operations"].(map[string]any)
	shareSemantics := shareOps["share"].(map[string]any)
	if shareSemantics["replay"] != string(ReplayRequestID) || shareSemantics["request_id_field"] != "request_id" {
		t.Fatalf("share operation metadata = %#v", shareSemantics)
	}
}
