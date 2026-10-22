//go:build linux

package secretbroker

import (
	"context"
	"errors"
	"net"
	"slices"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	provider "loki/modules/secrets/devtools"
)

// MigrationAdminOptions is installed only by the host-owned inactive layout.
// Ordinary active brokers have no migration comparison capability.
type MigrationAdminOptions struct {
	Provider                            *provider.Provider
	Operation, Policy, Runner, Instance string
	Resources                           []controlpolicy.ApplicationResource
}

func serveMigrationComparison(ctx context.Context, conn *net.UnixConn, options *MigrationAdminOptions, request appvalues.AdminRequest, body []byte) {
	result := appvalues.MigrationComparisonResult{Stage: "failed", Reason: "input"}
	defer func() { _ = privateio.WriteControl(conn, result) }()
	if options == nil || !uuidPattern.MatchString(options.Operation) || request.Revision != options.Policy || !uuidPattern.MatchString(request.RequestID) || request.Name != "" || request.Principal != "" || request.Variables != nil || request.Empty || request.Replace || len(request.Members) == 0 {
		return
	}
	var resource controlpolicy.ApplicationResource
	found := false
	for _, r := range options.Resources {
		if r.Alias == request.Resource {
			resource, found = r, true
			break
		}
	}
	if !found {
		return
	}
	values, err := privateio.DecodeMigrationExpected(body)
	if err != nil {
		return
	}
	defer privateio.ClearMigrationExpected(values)
	expected := make([]MigrationExpected, 0, len(values))
	names := make([]string, 0, len(values))
	for _, value := range values {
		kind, exists := resource.Keys[value.Name]
		if !exists {
			return
		}
		expected = append(expected, MigrationExpected{Name: value.Name, Kind: kind, Value: value.Value})
		names = append(names, value.Name)
	}
	if !slices.Equal(names, request.Members) {
		return
	}
	comparison, err := CompareMigration(ctx, MigrationComparisonOptions{Provider: options.Provider, Operation: options.Operation, Attempt: request.RequestID, Policy: options.Policy, Runner: options.Runner, Instance: privateio.OperationID(options.Instance, request.RequestID, "migration-instance"), Resource: resource, Environment: request.Environment, Expected: expected,
		Persist: func(binding MigrationComparisonBinding) error {
			if privateio.WriteControl(conn, appvalues.MigrationPrepared{Stage: "prepared", Binding: binding}) != nil {
				return errMigrationComparison
			}
			var ack appvalues.MigrationApproval
			if privateio.ReadControl(conn, &ack) != nil || ack.Stage != "approve" || ack.Attempt != binding.Attempt || ack.Execution != binding.Execution || ack.Input != binding.Input || ack.Revision != binding.Revision {
				return errMigrationComparison
			}
			return nil
		},
	})
	if err == nil && comparison.Matched {
		result = appvalues.MigrationComparisonResult{Stage: "matched", Matched: true, Count: comparison.Count}
	} else {
		result.Reason = "checkpoint"
		var failure *migrationComparisonFailure
		if errors.As(err, &failure) {
			result.Reason = failure.stage
		}
	}
}
