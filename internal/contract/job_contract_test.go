package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJobUsesGeneratedActionSpecificContract(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "job")
	if tool == nil {
		t.Fatal("job definition missing")
	}
	for _, phrase := range []string{"isolated executor", "Job", "backend instance"} {
		if !strings.Contains(tool.Description, phrase) {
			t.Fatalf("job description lacks %q: %q", phrase, tool.Description)
		}
	}

	encoded, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err = json.Unmarshal(encoded, &input); err != nil {
		t.Fatal(err)
	}
	properties := input["properties"].(map[string]any)
	for _, required := range []string{"action", "request_id", "cwd", "argv", "timeout_seconds", "network", "endpoints", "job_id"} {
		property, ok := properties[required].(map[string]any)
		if !ok {
			t.Fatalf("job input property %q missing", required)
		}
		if description, _ := property["description"].(string); strings.TrimSpace(description) == "" {
			t.Fatalf("job input property %q has no description", required)
		}
	}
	for _, forbidden := range []string{
		"policy_sha256", "request_sha256", "backend_ref", "instance_ref",
		"image", "mounts", "environment", "uid", "gid", "launcher_socket",
	} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("job exposes privileged input %q", forbidden)
		}
	}

	branches := input["oneOf"].([]any)
	if len(branches) != 4 {
		t.Fatalf("job input branches = %d, want 4", len(branches))
	}
	seen := map[string]bool{}
	for _, raw := range branches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("job input branch remains open: %#v", branch)
		}
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		seen[action] = true
		required := stringSet(branch["required"])
		switch action {
		case "start":
			if !required["request_id"] || !required["argv"] {
				t.Fatalf("start required = %#v", required)
			}
			for _, allowed := range []string{"action", "request_id", "cwd", "argv", "timeout_seconds", "network", "endpoints"} {
				if _, ok := branchProperties[allowed]; !ok {
					t.Fatalf("start omits %s", allowed)
				}
			}
			network := branchProperties["network"].(map[string]any)
			if !stringSet(network["enum"])["dependency-install"] || !stringSet(network["enum"])["none"] {
				t.Fatalf("start network enum = %#v", network["enum"])
			}
			endpoints := branchProperties["endpoints"].(map[string]any)
			endpoint := endpoints["items"].(map[string]any)
			if endpoint["additionalProperties"] != false {
				t.Fatalf("endpoint declaration remains open: %#v", endpoint)
			}
			if _, ok := endpoint["properties"].(map[string]any)["host_port"]; ok {
				t.Fatal("start endpoint accepts caller-chosen host_port")
			}
			if _, ok := branchProperties["job_id"]; ok {
				t.Fatal("start accepts caller-chosen job_id")
			}
		case "inspect", "output", "cancel":
			if !required["job_id"] {
				t.Fatalf("%s does not require job_id", action)
			}
			if len(branchProperties) != 2 {
				t.Fatalf("%s accepts unrelated fields: %#v", action, branchProperties)
			}
		default:
			t.Fatalf("unexpected job action %q", action)
		}
	}
	for _, action := range []string{"start", "inspect", "output", "cancel"} {
		if !seen[action] {
			t.Fatalf("job action %q missing", action)
		}
	}

	encoded, err = json.Marshal(tool.OutputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err = json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	outputBranches := output["oneOf"].([]any)
	if len(outputBranches) != 4 {
		t.Fatalf("job output branches = %d, want 4", len(outputBranches))
	}
	outputActions := map[string]bool{}
	for _, raw := range outputBranches {
		branch := raw.(map[string]any)
		if branch["additionalProperties"] != false {
			t.Fatalf("job output branch remains open: %#v", branch)
		}
		branchProperties := branch["properties"].(map[string]any)
		action := branchProperties["action"].(map[string]any)["const"].(string)
		outputActions[action] = true
		for _, forbidden := range []string{
			"policy_sha256", "request_sha256", "backend_ref", "instance_ref",
			"container_id", "image", "mounts", "environment", "uid", "gid",
		} {
			if hasSchemaKey(branch, forbidden) {
				t.Fatalf("%s output exposes privileged field %q", action, forbidden)
			}
		}
		switch action {
		case "start":
			for _, name := range []string{"request_id", "job_id", "state", "replayed", "detached", "deadline_at", "network", "endpoint_requests"} {
				if _, ok := branchProperties[name]; !ok {
					t.Fatalf("start output omits %s", name)
				}
			}
		case "inspect":
			for _, name := range []string{"job_id", "state", "created_at", "updated_at", "deadline_at", "network", "endpoints"} {
				if _, ok := branchProperties[name]; !ok {
					t.Fatalf("inspect output omits %s", name)
				}
			}
			endpoints := branchProperties["endpoints"].(map[string]any)
			lease := endpoints["items"].(map[string]any)
			if lease["additionalProperties"] != false {
				t.Fatalf("endpoint lease remains open: %#v", lease)
			}
			for _, name := range []string{"lease_id", "job_id", "name", "port", "host_port", "state", "created_at", "updated_at"} {
				if _, ok := lease["properties"].(map[string]any)[name]; !ok {
					t.Fatalf("endpoint lease omits %s", name)
				}
			}
		case "output":
			for _, name := range []string{"job_id", "state", "output", "truncated", "complete"} {
				if _, ok := branchProperties[name]; !ok {
					t.Fatalf("output result omits %s", name)
				}
			}
		case "cancel":
			status, ok := branchProperties["status"].(map[string]any)
			if !ok || status["additionalProperties"] != false {
				t.Fatalf("cancel status remains open: %#v", branchProperties["status"])
			}
		default:
			t.Fatalf("unexpected job output action %q", action)
		}
	}
	for _, action := range []string{"start", "inspect", "output", "cancel"} {
		if !outputActions[action] {
			t.Fatalf("job output action %q missing", action)
		}
	}

	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 4 {
		t.Fatalf("job operation metadata = %#v", tool.Meta)
	}
	startMeta := operations["start"].(map[string]any)
	if startMeta["replay"] != string(ReplayRequestID) ||
		startMeta["request_id_field"] != "request_id" ||
		startMeta["crash_recovery"] != string(CrashRecoveryJournaled) {
		t.Fatalf("job start semantics = %#v", startMeta)
	}
	for _, action := range []string{"inspect", "output", "cancel"} {
		meta := operations[action].(map[string]any)
		if meta["replay"] != string(ReplayIdempotent) {
			t.Fatalf("%s replay semantics = %#v", action, meta)
		}
	}
	cancelMeta := operations["cancel"].(map[string]any)
	if cancelMeta["crash_recovery"] != string(CrashRecoveryInspect) ||
		cancelMeta["recovery_reference"] != "job action=inspect" {
		t.Fatalf("cancel recovery semantics = %#v", cancelMeta)
	}
	if tool.Annotations == nil || tool.Annotations.ReadOnlyHint ||
		tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint ||
		tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
		t.Fatalf("job annotations = %#v", tool.Annotations)
	}
}

func stringSet(raw any) map[string]bool {
	result := map[string]bool{}
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				result[text] = true
			}
		}
	case []string:
		for _, value := range values {
			result[value] = true
		}
	}
	return result
}

func hasSchemaKey(value any, key string) bool {
	switch current := value.(type) {
	case map[string]any:
		if _, ok := current[key]; ok {
			return true
		}
		for _, nested := range current {
			if hasSchemaKey(nested, key) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if hasSchemaKey(nested, key) {
				return true
			}
		}
	}
	return false
}
