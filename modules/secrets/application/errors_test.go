//go:build linux

package application

import (
	"testing"

	"loki/internal/fault"
	provider "loki/modules/secrets/devtools"
)

func TestProviderFailuresRemainDistinctAndSafe(t *testing.T) {
	for code, want := range map[string]fault.Code{"value_expired": fault.CodeInputExpired, "value_exists": fault.CodeValueExists, "request_expired": fault.CodeRequestExpired, "vault_locked": fault.CodeVaultLocked, "integrity_failed": fault.CodeRecoveryRequired, "request_conflict": fault.CodeConflict} {
		detail := fault.Describe(safeError(&provider.Failure{Code: code, Reason: "untrusted-private-detail"}))
		if detail.Code != want {
			t.Fatalf("%s became %s", code, detail.Code)
		}
		if detail.Message == "" || detail.NextAction == "" {
			t.Fatal("error omitted its safe recovery guidance")
		}
	}
}
