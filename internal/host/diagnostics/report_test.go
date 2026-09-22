package diagnostics

import (
	"strings"
	"testing"
	"time"
)

func TestReportAggregatesWorstStatusAndCopiesEvidence(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 30, 0, 0, time.UTC)
	evidence := []Evidence{{Name: "generation", Value: "sha256:abc"}}
	report, err := NewReport(
		now,
		Healthy("host", "host_ready", "host lifecycle state is consistent", evidence...),
		Degraded("storage", "storage_near_limit", "retained storage is approaching its configured budget"),
		Blocked("runtime", "runtime_incomplete", "required runtime services are not all running"),
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence[0].Value = "mutated"
	if report.Status != StatusBlocked || report.Healthy() ||
		report.GeneratedAt != now.Format(time.RFC3339Nano) ||
		report.Checks[0].Evidence[0].Value != "sha256:abc" {
		t.Fatalf("report = %#v", report)
	}
}

func TestReportRejectsSensitiveOrUnboundedEvidence(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 30, 0, 0, time.UTC)
	for name, check := range map[string]Check{
		"sensitive-name": Healthy(
			"runtime", "runtime_ready", "runtime is ready",
			Evidence{Name: "access_token", Value: "redacted"},
		),
		"newline": Healthy(
			"runtime", "runtime_ready", "runtime is ready",
			Evidence{Name: "service", Value: "runtime\nsecret"},
		),
		"oversized": Healthy(
			"runtime", "runtime_ready", "runtime is ready",
			Evidence{Name: "service", Value: strings.Repeat("x", 257)},
		),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewReport(now, check); err == nil {
				t.Fatalf("unsafe diagnostic check accepted: %#v", check)
			}
		})
	}
}
