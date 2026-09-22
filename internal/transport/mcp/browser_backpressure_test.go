package mcptransport

import (
	"strings"
	"testing"

	"loki/internal/fault"
	"loki/internal/integrations/sharing/artifacts"
)

func TestBrowserShareCapacityErrorsAreRetryableQuota(t *testing.T) {
	for name, err := range map[string]error{
		"artifact": artifacts.ErrCapacityFull,
		"replay":   artifacts.ErrReplayCapacityFull,
	} {
		t.Run(name, func(t *testing.T) {
			detail := fault.Describe(browserShareReplayError(err))
			if detail.Code != fault.CodeQuotaExceeded || !detail.Retryable ||
				!strings.Contains(detail.NextAction, "retry") {
				t.Fatalf("browser share capacity detail = %#v", detail)
			}
		})
	}
}
