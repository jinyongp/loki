package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

func TestPrintHostUpdateImpactShowsConcretePreparedImpact(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	installed := hostGenerationFixture(t, now)
	available, err := lifecycle.NewGeneration(lifecycle.GenerationSpec{
		Version: "1.2.4", ReleasedAt: now,
		HostBinaryDigest: "sha256:" + strings.Repeat("c", 64),
		CoreImageDigest:  "sha256:" + strings.Repeat("d", 64),
		ConfigSchema:     1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: lifecycle.Compatibility{
			Config: lifecycle.SchemaRange{Min: 1, Max: 1}, Policy: lifecycle.SchemaRange{Min: 1, Max: 1},
			Toolchain: lifecycle.SchemaRange{Min: 1, Max: 1}, State: lifecycle.SchemaRange{Min: 1, Max: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	status := lifecycle.UpdateStatus{
		Installed: &installed, Available: &available,
		Prepared: &lifecycle.PreparedPlan{Impact: lifecycle.Impact{
			RestartRequired: true, MigrationRequired: false, RollbackCompatible: true,
		}},
	}
	var output bytes.Buffer
	if err = printHostUpdateImpact(&output, status); err != nil {
		t.Fatal(err)
	}
	body := output.String()
	for _, want := range []string{"1.2.3 -> 1.2.4", "Restart required: true", "Migration required: false", "Rollback compatible: true"} {
		if !strings.Contains(body, want) {
			t.Fatalf("impact output %q missing %q", body, want)
		}
	}
}
