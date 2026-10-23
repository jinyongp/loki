//go:build linux

package secretbroker

import (
	"log"

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
	log.Printf("application provider operation failed: operation=%s correlation=%s code=%s; %s", operation, correlation, d.Code, d.NextAction)
}
