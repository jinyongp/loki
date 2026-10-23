//go:build linux

package application

import (
	"loki/internal/fault"
	"loki/internal/secretoutput"
	provider "loki/modules/secrets/devtools"
	"strings"
	"testing"
)

func TestReviewedDiagnosticsPreserveRecoveryAndOutputPolicy(t *testing.T) {
	for _, test := range []struct {
		failure  provider.Failure
		code     fault.Code
		contains string
	}{
		{provider.Failure{Code: "vault_recovery_required", Retry: "same_request", RequestID: "2e66c180-f0b6-40ab-b09c-18f9df44b8c8"}, fault.CodeRecoveryRequired, "resume rewrap"},
		{provider.Failure{Code: "delivery_outcome_unknown", Retry: "inspect_first"}, fault.CodeDeliveryUnknown, "original Job"},
		{provider.Failure{Code: "redaction_unavailable", Limit: &secretoutput.LimitError{Kind: "states", Observed: 262145, Limit: 262144}}, fault.CodeLimitExceeded, "observed=262145 limit=262144"},
		{provider.Failure{Code: "key_backend_locked"}, fault.CodeVaultLocked, "administrator"},
	} {
		d := fault.Describe(safeError(&test.failure))
		if d.Code != test.code || !strings.Contains(d.NextAction, test.contains) {
			t.Fatalf("unsafe or missing recovery guidance: %+v", d)
		}
	}
}
