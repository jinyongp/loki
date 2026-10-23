//go:build linux

package secretbroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	provider "loki/modules/secrets/devtools"
)

var errMigrationComparison = errors.New("private migration comparison did not complete")

type migrationComparisonFailure struct{ stage string }

func (e *migrationComparisonFailure) Error() string {
	return "private migration comparison failed at " + e.stage
}
func (e *migrationComparisonFailure) Unwrap() error { return errMigrationComparison }
func migrationFailure(stage string) error           { return &migrationComparisonFailure{stage: stage} }

// MigrationExpected is private borrowed input supplied by the root host
// migration channel. It is never part of an operation record or public result.
type MigrationExpected struct {
	Name  string
	Kind  string
	Value []byte
}

func (MigrationExpected) Format(s fmt.State, _ rune)   { io.WriteString(s, "[REDACTED]") }
func (MigrationExpected) MarshalJSON() ([]byte, error) { return nil, errMigrationComparison }
func (MigrationExpected) MarshalText() ([]byte, error) { return nil, errMigrationComparison }

// MigrationComparisonBinding is the immutable private delivery correlation
// checkpointed by the original host operation BEFORE approving any delivery.
// CorrelationID identifies this read-only validation attempt, not an OCI Job.
type MigrationComparisonBinding = appvalues.MigrationComparisonBinding

type MigrationComparisonOptions struct {
	Provider                 *provider.Provider
	Operation, Attempt       string
	Runner, Instance, Policy string
	Resource                 controlpolicy.ApplicationResource
	Environment              string
	Expected                 []MigrationExpected
	Persist                  func(MigrationComparisonBinding) error
}

type MigrationComparisonResult struct {
	Matched bool `json:"matched"`
	Count   int  `json:"count"`
}

// CompareMigration runs in the staged broker, through the same reviewed private
// receiver as Jobs but with a separate migration runner/correlation authority.
// It never constructs a Docker reservation or reports invented execution facts.
// A lost attempt is revalidated with a fresh Attempt/Instance; target writes use
// their separate stable slot request IDs in the original host operation.
func CompareMigration(ctx context.Context, options MigrationComparisonOptions) (result MigrationComparisonResult, resultErr error) {
	if options.Provider == nil || !uuidPattern.MatchString(options.Operation) || !uuidPattern.MatchString(options.Attempt) || !uuidPattern.MatchString(options.Runner) || !uuidPattern.MatchString(options.Instance) || !digestPattern.MatchString(options.Policy) || options.Persist == nil || len(options.Expected) == 0 || len(options.Expected) > controlpolicy.MaxApplicationKeys {
		return result, migrationFailure("input")
	}
	resources, err := controlpolicy.CanonicalApplications([]controlpolicy.ApplicationResource{options.Resource}, nil)
	if err != nil {
		return result, migrationFailure("policy")
	}
	resource := resources[0]
	if options.Environment == "" {
		options.Environment = "common"
	}
	allowed := false
	for _, env := range resource.Environments {
		allowed = allowed || env == options.Environment
	}
	if !allowed {
		return result, migrationFailure("environment")
	}
	expected := make(map[string]MigrationExpected, len(options.Expected))
	keys := make([]string, 0, len(options.Expected))
	for _, v := range options.Expected {
		if _, duplicate := expected[v.Name]; duplicate || (v.Kind != "secret" && v.Kind != "variable") || resource.Keys[v.Name] != v.Kind || len(v.Value) > 65536 || !utf8.Valid(v.Value) || strings.ContainsRune(string(v.Value), 0) {
			return result, migrationFailure("values")
		}
		expected[v.Name] = v
		keys = append(keys, v.Name)
	}
	sort.Strings(keys)
	// Offline host comparison performs several durable provider operations.
	// Its cleanup must also complete on a CPU-emulated recovery host; the
	// ordinary Job execution/admission budgets do not apply to this channel.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	op := func(action string) string {
		return privateio.OperationID(options.Operation, options.Attempt, "migration-compare/"+action)
	}
	correlation := strings.ReplaceAll(op("correlation"), "-", "")
	a, err := options.Provider.Address(ctx, options.Runner, options.Instance)
	if err != nil {
		return result, migrationFailure("address")
	}
	execution, err := options.Provider.Prepare(ctx, resource.Profile(), options.Environment, correlation, op("prepare"), "", keys)
	if err != nil {
		reportProviderFailure("migration_prepare", correlation, err)
		return result, migrationFailure("prepare")
	}
	delivered := false
	defer func() {
		if !delivered {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			_, _ = options.Provider.Cancel(cleanup, resource.Profile(), op("cancel"), execution)
		}
	}()
	binding := MigrationComparisonBinding{Operation: options.Operation, Attempt: options.Attempt, Policy: options.Policy,
		Resource: resource.ID, Environment: options.Environment, Runner: options.Runner, Instance: options.Instance,
		Names: keys, RootID: a.RootID, VaultID: a.VaultID, Execution: execution.ID, Input: execution.InputID, CorrelationID: correlation, Revision: execution.Revision}
	if err := options.Persist(binding); err != nil {
		return result, err
	}
	address := Address(a)
	listener, err := Listen(address)
	if err != nil {
		return result, migrationFailure("listener")
	}
	received := make(chan struct{}, 1)
	r := Receiver{Address: address,
		Admit: func(_ context.Context, identity InputIdentity) error {
			if identity.ExecutionID != binding.Execution || identity.InputID != binding.Input || identity.ExternalExecutionID != binding.CorrelationID {
				return errMigrationComparison
			}
			return nil
		},
		Accept: func(_ context.Context, input *ReceivedInput) error {
			return input.WithValues(func(values map[string]string, kinds map[string]string) error {
				if len(values) != len(expected) {
					return errMigrationComparison
				}
				for name, want := range expected {
					value, exists := values[name]
					if !exists || kinds[name] != want.Kind || value != string(want.Value) {
						return errMigrationComparison
					}
				}
				return nil
			})
		},
		Handle: func(context.Context, *ReceivedInput) error {
			select {
			case received <- struct{}{}:
			default:
			}
			return nil
		},
	}
	receiverCtx, stopReceiver := context.WithCancel(ctx)
	receiverDone := make(chan struct{})
	go func() { defer close(receiverDone); _ = r.Serve(receiverCtx, listener) }()
	defer func() { stopReceiver(); listener.Close(); <-receiverDone }()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := options.Provider.Unregister(cleanup, options.Runner, options.Instance, op("unregister")); err != nil && resultErr == nil {
			result, resultErr = MigrationComparisonResult{}, migrationFailure("unregister")
		}
	}()
	if options.Provider.Register(ctx, options.Runner, options.Instance, op("register")) != nil {
		return result, migrationFailure("register")
	}
	next, err := options.Provider.Approve(ctx, resource.Profile(), options.Runner, options.Instance, op("approve"), execution)
	if err != nil {
		reportProviderFailure("migration_approve", correlation, err)
		return result, migrationFailure("approve")
	}
	execution = next
	next, err = options.Provider.Deliver(ctx, resource.Profile(), op("deliver"), execution)
	if err != nil {
		reportProviderFailure("migration_deliver", correlation, err)
		return result, migrationFailure("deliver")
	}
	execution = next
	delivered = true
	select {
	case <-received:
		return MigrationComparisonResult{Matched: true, Count: len(expected)}, nil
	case <-ctx.Done():
		return result, migrationFailure("receipt")
	}
}
