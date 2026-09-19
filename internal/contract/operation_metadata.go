package contract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const RequestIDPattern = "^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"

func RequestIDSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"pattern":     RequestIDPattern,
		"description": description,
	}
}

type ReplayPolicy string

const (
	ReplayIdempotent ReplayPolicy = "idempotent"
	ReplayRequestID  ReplayPolicy = "request_id"
	ReplayGuarded    ReplayPolicy = "guarded"
	ReplayUnsafe     ReplayPolicy = "unsafe"
)

type FailureAtomicity string

const (
	FailureSingleResource FailureAtomicity = "single_resource"
	FailurePreflight      FailureAtomicity = "preflight"
	FailureRollback       FailureAtomicity = "rollback"
	FailureUpstream       FailureAtomicity = "upstream"
	FailureNone           FailureAtomicity = "none"
)

type CrashRecovery string

const (
	CrashRecoveryNone      CrashRecovery = "none"
	CrashRecoveryInspect   CrashRecovery = "inspect"
	CrashRecoveryJournaled CrashRecovery = "journaled"
	CrashRecoveryUpstream  CrashRecovery = "upstream"
)

type OperationSemantics struct {
	Replay                ReplayPolicy
	RequestIDField        string
	ConcurrencyFields     []string
	FailureAtomicity      FailureAtomicity
	CrashRecovery         CrashRecovery
	AffectedResourceLimit int
	RecoveryReference     string
}

func validReplayPolicy(value ReplayPolicy) bool {
	switch value {
	case ReplayIdempotent, ReplayRequestID, ReplayGuarded, ReplayUnsafe:
		return true
	default:
		return false
	}
}

func validFailureAtomicity(value FailureAtomicity) bool {
	switch value {
	case FailureSingleResource, FailurePreflight, FailureRollback, FailureUpstream, FailureNone:
		return true
	default:
		return false
	}
}

func validCrashRecovery(value CrashRecovery) bool {
	switch value {
	case CrashRecoveryNone, CrashRecoveryInspect, CrashRecoveryJournaled, CrashRecoveryUpstream:
		return true
	default:
		return false
	}
}

func (s OperationSemantics) metadata(properties map[string]any) (map[string]any, error) {
	if !validReplayPolicy(s.Replay) {
		return nil, fmt.Errorf("operation semantics require a valid replay policy")
	}
	if !validFailureAtomicity(s.FailureAtomicity) {
		return nil, fmt.Errorf("operation semantics require valid failure atomicity")
	}
	if !validCrashRecovery(s.CrashRecovery) {
		return nil, fmt.Errorf("operation semantics require valid crash recovery")
	}
	if s.AffectedResourceLimit < 1 {
		return nil, fmt.Errorf("operation semantics require a positive affected-resource limit")
	}
	if s.Replay == ReplayRequestID {
		if strings.TrimSpace(s.RequestIDField) == "" {
			return nil, fmt.Errorf("request-id replay requires a request ID field")
		}
		if _, ok := properties[s.RequestIDField]; !ok {
			return nil, fmt.Errorf("request ID field %q is not present in the tool schema", s.RequestIDField)
		}
	} else if s.RequestIDField != "" {
		return nil, fmt.Errorf("request ID field is valid only for request-id replay")
	}
	seen := map[string]bool{}
	guards := append([]string(nil), s.ConcurrencyFields...)
	for _, field := range guards {
		if field == "" || seen[field] {
			return nil, fmt.Errorf("operation semantics contain an invalid concurrency field")
		}
		if _, ok := properties[field]; !ok {
			return nil, fmt.Errorf("concurrency field %q is not present in the tool schema", field)
		}
		seen[field] = true
	}
	if s.Replay == ReplayGuarded && len(guards) == 0 {
		return nil, fmt.Errorf("guarded replay requires a concurrency field")
	}
	sort.Strings(guards)

	metadata := map[string]any{
		"replay":                  string(s.Replay),
		"failure_atomicity":       string(s.FailureAtomicity),
		"crash_recovery":          string(s.CrashRecovery),
		"affected_resource_limit": s.AffectedResourceLimit,
	}
	if s.RequestIDField != "" {
		metadata["request_id_field"] = s.RequestIDField
	}
	if len(guards) > 0 {
		metadata["concurrency_fields"] = guards
	}
	if s.RecoveryReference != "" {
		metadata["recovery_reference"] = s.RecoveryReference
	}
	return metadata, nil
}

func ApplyOperationMetadata(tool *mcp.Tool, operations map[string]OperationSemantics) error {
	if tool == nil || tool.Name == "" {
		return fmt.Errorf("operation metadata requires a tool")
	}
	if len(operations) == 0 {
		return fmt.Errorf("operation metadata requires at least one operation")
	}
	input, err := schemaMap(tool.InputSchema)
	if err != nil {
		return fmt.Errorf("decode input schema for %s: %w", tool.Name, err)
	}
	properties, _ := input["properties"].(map[string]any)
	allowedActions := map[string]bool{}
	if action, ok := properties["action"].(map[string]any); ok {
		for _, value := range actionEnum(action) {
			name, _ := value.(string)
			if name != "" {
				allowedActions[name] = true
			}
		}
	}

	names := make([]string, 0, len(operations))
	for name := range operations {
		names = append(names, name)
	}
	sort.Strings(names)
	encoded := map[string]any{}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("operation metadata has an empty operation name")
		}
		if len(allowedActions) > 0 && !allowedActions[name] {
			return fmt.Errorf("operation %q is not declared by tool %s", name, tool.Name)
		}
		metadata, err := operations[name].metadata(properties)
		if err != nil {
			return fmt.Errorf("%s operation %s: %w", tool.Name, name, err)
		}
		encoded[name] = metadata
	}
	if tool.Meta == nil {
		tool.Meta = mcp.Meta{}
	}
	tool.Meta["loki/operations"] = encoded
	return nil
}
