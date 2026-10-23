package contract

import "github.com/modelcontextprotocol/go-sdk/mcp"

func overrideApplicationSecret(tool *mcp.Tool) error {
	input, err := SecretV2InputSchema(tool.Name)
	if err != nil {
		return err
	}
	tool.InputSchema, tool.OutputSchema = input, SecretV2OutputSchema(tool.Name)
	read := tool.Name == "secret_inspect"
	tool.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: read, DestructiveHint: boolPointer(!read), IdempotentHint: true, OpenWorldHint: boolPointer(false)}
	operations := map[string]OperationSemantics{}
	if read {
		tool.Description = "Inspect granted application resources, value names/kinds/sources, imports, protection status or metadata audit. Secret values are never returned. Use opaque revision-bound cursors; omission of env selects common."
		for _, action := range []string{"resources", "values", "imports", "status", "audit"} {
			operations[action] = OperationSemantics{Replay: ReplayIdempotent, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect, AffectedResourceLimit: 1, RecoveryReference: "repeat the metadata query; refresh a stale cursor"}
		}
	} else {
		tool.Description = "Mutate one administrator-granted application resource using its opaque expected_revision and a caller-owned request_id. set_public accepts public configuration; generate creates exactly 32 private bytes; import_staged consumes private administrator-staged values. unset removes the exact kind/environment layer and may reveal common fallback. Never send credentials over MCP."
		actions := []string{"set_public", "generate", "import_staged"}
		if tool.Name == "secret_delete" {
			actions = []string{"unset"}
		}
		for _, action := range actions {
			operations[action] = OperationSemantics{Replay: ReplayRequestID, RequestIDField: "request_id", ConcurrencyFields: []string{"expected_revision"}, FailureAtomicity: FailureSingleResource, CrashRecovery: CrashRecoveryInspect, AffectedResourceLimit: 1, RecoveryReference: "retry identical input with the same request_id; inspect values/imports metadata before a new request"}
		}
	}
	return ApplyOperationMetadata(tool, operations)
}
