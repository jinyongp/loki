package windows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	ConnectionTaskOwnershipSchemaVersion       = 1
	connectionTaskDescription                  = "Restore enabled Loki managed connections."
	connectionTaskExecutionTicks         int64 = 6_000_000_000
)

type ConnectionTaskOwnership struct {
	SchemaVersion int    `json:"schema_version"`
	Distribution  string `json:"distribution"`
	TaskName      string `json:"task_name"`
	Executable    string `json:"executable"`
	Arguments     string `json:"arguments"`
	Description   string `json:"description"`
}

type ConnectionTaskProbe struct {
	Present            bool
	Description        string
	Actions            []StartupTaskAction
	RunLevel           string
	UserID             string
	TriggerCount       int
	LogonTrigger       bool
	ExecutionTimeTicks int64
}

type ConnectionTaskPlatform interface {
	Probe(context.Context, string) (ConnectionTaskProbe, error)
	Create(context.Context, ConnectionTaskOwnership) error
	Remove(context.Context, ConnectionTaskOwnership) error
	ReadOwnership(string) (ConnectionTaskOwnership, bool, error)
	WriteOwnership(context.Context, ConnectionTaskOwnership) error
	DeleteOwnership(context.Context, string) error
	VerifyCanonicalFrontend(context.Context) (FrontendPaths, error)
	CurrentUser() (string, error)
}

type ConnectionTaskManager struct {
	Platform ConnectionTaskPlatform
}

func (manager ConnectionTaskManager) Reconcile(ctx context.Context, distribution string, enabled bool) error {
	if manager.Platform == nil {
		return errors.New("managed connection startup-task platform is unavailable")
	}
	if err := ValidateDistributionName(distribution); err != nil {
		return err
	}
	paths, err := manager.Platform.VerifyCanonicalFrontend(ctx)
	if err != nil {
		return err
	}
	userID, err := manager.Platform.CurrentUser()
	if err != nil {
		return err
	}
	expected := expectedConnectionTask(paths, distribution)
	ownership, owned, err := manager.Platform.ReadOwnership(distribution)
	if err != nil {
		return err
	}
	if owned {
		if err = validateConnectionTaskOwnership(ownership, expected); err != nil {
			return err
		}
	}
	probe, err := manager.Platform.Probe(ctx, expected.TaskName)
	if err != nil {
		return err
	}
	if enabled {
		created := false
		switch {
		case probe.Present && !owned:
			return errors.New("refusing to adopt an unowned Loki connection startup task")
		case probe.Present:
			if err = validateConnectionTaskProbe(probe, expected, userID); err != nil {
				return err
			}
			return nil
		case !probe.Present && owned:
			if err = manager.Platform.Create(ctx, expected); err != nil {
				return err
			}
			created = true
		default:
			if err = manager.Platform.Create(ctx, expected); err != nil {
				return err
			}
			created = true
			if err = manager.Platform.WriteOwnership(ctx, expected); err != nil {
				_ = manager.Platform.Remove(ctx, expected)
				return err
			}
			owned = true
		}
		probe, err = manager.Platform.Probe(ctx, expected.TaskName)
		if err != nil {
			if created {
				_ = manager.Platform.Remove(ctx, expected)
			}
			return err
		}
		if err = validateConnectionTaskProbe(probe, expected, userID); err != nil {
			if created {
				_ = manager.Platform.Remove(ctx, expected)
			}
			return err
		}
		return nil
	}

	if probe.Present {
		if !owned {
			return errors.New("refusing to remove an unowned Loki connection startup task")
		}
		if err = validateConnectionTaskProbe(probe, expected, userID); err != nil {
			return err
		}
		if err = manager.Platform.Remove(ctx, expected); err != nil {
			return err
		}
	}
	if owned {
		if err = manager.Platform.DeleteOwnership(ctx, distribution); err != nil {
			return err
		}
	}
	return nil
}

func expectedConnectionTask(paths FrontendPaths, distribution string) ConnectionTaskOwnership {
	return ConnectionTaskOwnership{
		SchemaVersion: ConnectionTaskOwnershipSchemaVersion,
		Distribution:  distribution,
		TaskName:      "Loki Connections (" + distribution + ")",
		Executable:    paths.Binary,
		Arguments:     "connect startup --distribution " + distribution,
		Description:   connectionTaskDescription,
	}
}

func validateConnectionTaskOwnership(actual, expected ConnectionTaskOwnership) error {
	if actual.SchemaVersion != ConnectionTaskOwnershipSchemaVersion ||
		actual.Distribution != expected.Distribution ||
		actual.TaskName != expected.TaskName ||
		!WindowsPathEqual(actual.Executable, expected.Executable) ||
		actual.Arguments != expected.Arguments ||
		actual.Description != expected.Description {
		return errors.New("managed connection startup-task ownership does not match the canonical task")
	}
	return nil
}

func validateConnectionTaskProbe(probe ConnectionTaskProbe, expected ConnectionTaskOwnership, userID string) error {
	// Report field names, never task arguments or provider-controlled values.
	var mismatches []string
	if !probe.Present {
		mismatches = append(mismatches, "presence")
	}
	if len(probe.Actions) != 1 {
		mismatches = append(mismatches, "action_count")
	} else {
		if !WindowsPathEqual(probe.Actions[0].Executable, expected.Executable) {
			mismatches = append(mismatches, "executable")
		}
		if probe.Actions[0].Arguments != expected.Arguments {
			mismatches = append(mismatches, "arguments")
		}
	}
	if probe.Description != expected.Description {
		mismatches = append(mismatches, "description")
	}
	if !strings.EqualFold(probe.RunLevel, "Limited") {
		mismatches = append(mismatches, "run_level")
	}
	if strings.TrimSpace(userID) == "" || !strings.EqualFold(strings.TrimSpace(probe.UserID), strings.TrimSpace(userID)) {
		mismatches = append(mismatches, "principal")
	}
	if probe.TriggerCount != 1 {
		mismatches = append(mismatches, "trigger_count")
	}
	if !probe.LogonTrigger {
		mismatches = append(mismatches, "logon_trigger")
	}
	if probe.ExecutionTimeTicks != connectionTaskExecutionTicks {
		mismatches = append(mismatches, "execution_limit")
	}
	if len(mismatches) != 0 {
		return fmt.Errorf("Loki connection startup task does not match exact managed ownership: %s", strings.Join(mismatches, ", "))
	}
	return nil
}

func encodeConnectionTaskOwnership(ownership ConnectionTaskOwnership) ([]byte, error) {
	if ownership.SchemaVersion != ConnectionTaskOwnershipSchemaVersion ||
		ValidateDistributionName(ownership.Distribution) != nil ||
		ownership.TaskName == "" || ownership.Executable == "" ||
		ownership.Arguments == "" || ownership.Description != connectionTaskDescription {
		return nil, errors.New("managed connection startup-task ownership is invalid")
	}
	raw, err := json.MarshalIndent(ownership, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func parseConnectionTaskOwnership(raw []byte) (ConnectionTaskOwnership, error) {
	var ownership ConnectionTaskOwnership
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ownership); err != nil {
		return ConnectionTaskOwnership{}, fmt.Errorf("decode managed connection startup-task ownership: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return ConnectionTaskOwnership{}, err
	}
	if _, err := encodeConnectionTaskOwnership(ownership); err != nil {
		return ConnectionTaskOwnership{}, err
	}
	return ownership, nil
}
