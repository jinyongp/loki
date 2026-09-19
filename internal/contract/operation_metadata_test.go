package contract

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixtureOperationTool(t *testing.T) *mcp.Tool {
	t.Helper()
	input, err := (ActionInputContract{
		Title:             "fixtureArguments",
		ActionDescription: "Fixture mutation.",
		Fields: []ActionField{
			{Name: "request_id", Schema: RequestIDSchema("Caller-owned request identifier used for replay-safe mutation.")},
			{Name: "expected_sha256", Schema: map[string]any{
				"type": "string", "pattern": "^[0-9a-f]{64}$",
				"description": "Observed resource digest used as a concurrency precondition.",
			}},
		},
		Variants: []ActionVariant{
			{Name: "publish", Required: []string{"request_id"}},
			{Name: "replace", Required: []string{"expected_sha256"}},
		},
	}).Schema()
	if err != nil {
		t.Fatal(err)
	}
	return &mcp.Tool{Name: "fixture", InputSchema: input}
}

func TestRequestIDSchemaUsesSharedPattern(t *testing.T) {
	schema := RequestIDSchema("Replay key.")
	if schema["pattern"] != RequestIDPattern || schema["description"] != "Replay key." {
		t.Fatalf("request ID schema = %#v", schema)
	}
}

func TestApplyOperationMetadataPublishesReplayAndRecoverySemantics(t *testing.T) {
	tool := fixtureOperationTool(t)
	err := ApplyOperationMetadata(tool, map[string]OperationSemantics{
		"publish": {
			Replay: ReplayRequestID, RequestIDField: "request_id",
			FailureAtomicity: FailureRollback, CrashRecovery: CrashRecoveryJournaled,
			AffectedResourceLimit: 3, RecoveryReference: "system_inspect action=operation",
		},
		"replace": {
			Replay: ReplayGuarded, ConcurrencyFields: []string{"expected_sha256"},
			FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect,
			AffectedResourceLimit: 1, RecoveryReference: "restore revision",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok {
		t.Fatalf("operation metadata = %#v", tool.Meta)
	}
	publish := operations["publish"].(map[string]any)
	if publish["replay"] != string(ReplayRequestID) || publish["request_id_field"] != "request_id" ||
		publish["failure_atomicity"] != string(FailureRollback) || publish["crash_recovery"] != string(CrashRecoveryJournaled) {
		t.Fatalf("publish metadata = %#v", publish)
	}
	replace := operations["replace"].(map[string]any)
	guards := replace["concurrency_fields"].([]string)
	if len(guards) != 1 || guards[0] != "expected_sha256" || replace["affected_resource_limit"] != 1 {
		t.Fatalf("replace metadata = %#v", replace)
	}
}

func TestApplyOperationMetadataRejectsUntruthfulDeclarations(t *testing.T) {
	cases := []struct {
		name       string
		operations map[string]OperationSemantics
		contains   string
	}{
		{
			name: "undeclared action",
			operations: map[string]OperationSemantics{"missing": {
				Replay: ReplayUnsafe, FailureAtomicity: FailureNone, CrashRecovery: CrashRecoveryNone, AffectedResourceLimit: 1,
			}},
			contains: "not declared",
		},
		{
			name: "missing request field",
			operations: map[string]OperationSemantics{"publish": {
				Replay: ReplayRequestID, RequestIDField: "missing",
				FailureAtomicity: FailureRollback, CrashRecovery: CrashRecoveryJournaled, AffectedResourceLimit: 1,
			}},
			contains: "not present",
		},
		{
			name: "guarded without guard",
			operations: map[string]OperationSemantics{"replace": {
				Replay:           ReplayGuarded,
				FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect, AffectedResourceLimit: 1,
			}},
			contains: "requires a concurrency field",
		},
		{
			name: "unknown concurrency field",
			operations: map[string]OperationSemantics{"replace": {
				Replay: ReplayGuarded, ConcurrencyFields: []string{"missing"},
				FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect, AffectedResourceLimit: 1,
			}},
			contains: "not present",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			tool := fixtureOperationTool(t)
			err := ApplyOperationMetadata(tool, test.operations)
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want %q", err, test.contains)
			}
		})
	}
}

func TestGitHubIssueFieldsWritePublishesCurrentUnsafeReplaySemantics(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	tool := seenDefinition(definitions, "github_issue_fields_write")
	if tool == nil {
		t.Fatal("github_issue_fields_write definition missing")
	}
	operations, ok := tool.Meta["loki/operations"].(map[string]any)
	if !ok || len(operations) != 3 {
		t.Fatalf("operation metadata = %#v", tool.Meta)
	}
	for _, name := range []string{"add_values", "set_values", "clear_value"} {
		metadata, ok := operations[name].(map[string]any)
		if !ok {
			t.Fatalf("%s metadata = %#v", name, operations[name])
		}
		if metadata["replay"] != string(ReplayUnsafe) ||
			metadata["failure_atomicity"] != string(FailureUpstream) ||
			metadata["crash_recovery"] != string(CrashRecoveryUpstream) ||
			metadata["recovery_reference"] != "system_inspect action=operation" {
			t.Fatalf("%s metadata = %#v", name, metadata)
		}
	}
}
