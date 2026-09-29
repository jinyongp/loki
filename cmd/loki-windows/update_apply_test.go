package main

import (
	"strings"
	"testing"
)

func machineGenerationForUpdateTest(version string) *machineGeneration {
	generation := &machineGeneration{}
	generation.ID = "sha256:test-" + version
	generation.Spec.Version = version
	return generation
}

func TestUpdateApplyReadinessReportsCurrentReleaseWithoutPrepareInstruction(t *testing.T) {
	status := machineUpdateStatus{
		Installed:       machineGenerationForUpdateTest("0.1.27"),
		Available:       machineGenerationForUpdateTest("0.1.27"),
		UpdateAvailable: false,
	}
	err := updateApplyReadinessError(status)
	if err == nil || !strings.Contains(err.Error(), "no newer Loki appliance release") ||
		!strings.Contains(err.Error(), "v0.1.27") || strings.Contains(err.Error(), "prepare") {
		t.Fatalf("readiness error = %v", err)
	}
}

func TestUpdateApplyReadinessRequiresPrepareOnlyWhenUpdateIsAvailable(t *testing.T) {
	status := machineUpdateStatus{
		Installed:       machineGenerationForUpdateTest("0.1.26"),
		Available:       machineGenerationForUpdateTest("0.1.27"),
		UpdateAvailable: true,
	}
	err := updateApplyReadinessError(status)
	if err == nil || !strings.Contains(err.Error(), "available but not prepared") ||
		!strings.Contains(err.Error(), "loki update prepare") {
		t.Fatalf("readiness error = %v", err)
	}
}

func TestUpdateApplyReadinessAcceptsPreparedAdvancingUpdate(t *testing.T) {
	status := machineUpdateStatus{
		Installed:       machineGenerationForUpdateTest("0.1.26"),
		Available:       machineGenerationForUpdateTest("0.1.27"),
		Prepared:        &machinePreparedPlan{ID: "sha256:plan"},
		UpdateAvailable: true,
	}
	if err := updateApplyReadinessError(status); err != nil {
		t.Fatalf("prepared update rejected: %v", err)
	}
}
