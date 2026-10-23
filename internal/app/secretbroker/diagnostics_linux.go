//go:build linux

package secretbroker

import (
	"log"

	"loki/internal/fault"
	"loki/modules/secrets/application"
)

func reportProviderFailure(operation, correlation string, err error) {
	if err == nil || !idPattern.MatchString(correlation) {
		return
	}
	switch operation {
	case "prepare", "approve", "deliver", "migration_prepare", "migration_approve", "migration_deliver":
	default:
		return
	}
	// This journal belongs to the operator. Keep the public Job wire closed,
	// and use only validated correlation metadata plus the shared safe policy.
	d := application.ProviderDiagnostic(err)
	switch d.Code {
	case fault.CodeVaultLocked, fault.CodeRecoveryRequired, fault.CodeLimitExceeded, fault.CodeDenied:
		// These remedies concern protection or bounded input, not replay.
	default:
		// An unstructured transport failure can also hide committed delivery.
		// Administrative mutation retry guidance must not reach Job operators.
		d.NextAction = "inspect the original Job and execution/delivery record; do not prepare or send another execution"
		switch operation {
		case "migration_prepare", "migration_approve", "migration_deliver":
			// Comparison attempts have a durable binding, not an OCI Job.
			// Host recovery may start a fresh comparison attempt; this does
			// not authorize resending the original uncertain delivery.
			d.NextAction = "inspect the original migration operation, comparison binding and delivery record; do not resend the original delivery"
		}
	}
	log.Printf("application provider operation failed: operation=%s correlation=%s code=%s; %s", operation, correlation, d.Code, d.NextAction)
}
