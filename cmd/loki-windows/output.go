package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	windowshost "loki/internal/host/windows"
)

type machineDoctorEvidence struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type machineDoctorCheck struct {
	Name     string                  `json:"name"`
	Status   string                  `json:"status"`
	Summary  string                  `json:"summary"`
	Evidence []machineDoctorEvidence `json:"evidence,omitempty"`
}

type machineDoctorReport struct {
	Status string               `json:"status"`
	Checks []machineDoctorCheck `json:"checks"`
}

type machineGeneration struct {
	ID   string `json:"id"`
	Spec struct {
		Version string `json:"version"`
	} `json:"spec"`
}

type machineImpact struct {
	RestartRequired    bool `json:"restart_required"`
	MigrationRequired  bool `json:"migration_required"`
	RollbackCompatible bool `json:"rollback_compatible"`
}

type machinePreparedPlan struct {
	ID                    string        `json:"id"`
	ActiveGenerationID    string        `json:"active_generation_id,omitempty"`
	CandidateGenerationID string        `json:"candidate_generation_id"`
	Impact                machineImpact `json:"impact"`
}

type machineUpdateStatus struct {
	Installed       *machineGeneration   `json:"installed,omitempty"`
	Available       *machineGeneration   `json:"available,omitempty"`
	Prepared        *machinePreparedPlan `json:"prepared,omitempty"`
	UpdateAvailable bool                 `json:"update_available"`
}

type machineApplyResult struct {
	PlanID          string   `json:"plan_id"`
	InterruptedJobs []string `json:"interrupted_jobs,omitempty"`
}

type machineBackupRecord struct {
	ID        string             `json:"id"`
	CreatedAt time.Time          `json:"created_at"`
	Installed *machineGeneration `json:"installed,omitempty"`
}

func nativeProbeExitCode(probe windowshost.NativeProbe) int {
	if probe.ExitCode == 0 {
		return 0
	}
	if probe.ExitCode < 0 {
		return 1
	}
	return probe.ExitCode
}

func writeMachineJSON(stdout io.Writer, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("Loki appliance returned empty machine output")
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("decode Loki appliance machine output: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stdout, raw)
	return err
}

func decodeMachineJSON[T any](raw string) (T, error) {
	var result T
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode Loki appliance machine output: %w", err)
	}
	if err := requireJSONEnd(decoder); err != nil {
		return result, err
	}
	return result, nil
}

func requireJSONEnd(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("Loki appliance machine output contains trailing data")
}

func renderDoctor(raw string, stdout io.Writer) error {
	report, err := decodeMachineJSON[machineDoctorReport](raw)
	if err != nil {
		return err
	}
	if !validDoctorStatus(report.Status) || len(report.Checks) == 0 {
		return errors.New("Loki appliance doctor output is invalid")
	}
	fmt.Fprintf(stdout, "Loki doctor: %s\n", report.Status)
	for _, check := range report.Checks {
		if !validDoctorStatus(check.Status) || strings.TrimSpace(check.Name) == "" || strings.TrimSpace(check.Summary) == "" {
			return errors.New("Loki appliance doctor check is invalid")
		}
		fmt.Fprintf(stdout, "  %s: %s - %s\n", check.Name, check.Status, check.Summary)
		for _, evidence := range check.Evidence {
			fmt.Fprintf(stdout, "    %s: %s\n", evidence.Name, evidence.Value)
		}
	}
	return nil
}

func validDoctorStatus(value string) bool {
	switch strings.TrimSpace(value) {
	case "healthy", "degraded", "blocked":
		return true
	default:
		return false
	}
}

func renderUpdateStatus(raw string, stdout io.Writer) error {
	status, err := decodeMachineJSON[machineUpdateStatus](raw)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Loki update status")
	fmt.Fprintf(stdout, "  Installed: %s\n", generationVersion(status.Installed))
	fmt.Fprintf(stdout, "  Available: %s\n", generationVersion(status.Available))
	fmt.Fprintf(stdout, "  Update available: %s\n", yesNo(status.UpdateAvailable))
	if status.Prepared == nil {
		fmt.Fprintln(stdout, "  Prepared: no")
	} else {
		fmt.Fprintf(stdout, "  Prepared: yes (%s)\n", shortDigest(status.Prepared.ID))
	}
	return nil
}

func renderPreparedUpdate(raw string, stdout io.Writer) error {
	plan, err := decodeMachineJSON[machinePreparedPlan](raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.CandidateGenerationID) == "" {
		return errors.New("Loki appliance prepared update output is invalid")
	}
	fmt.Fprintln(stdout, "Prepared Loki update")
	fmt.Fprintf(stdout, "  Plan: %s\n", shortDigest(plan.ID))
	fmt.Fprintf(stdout, "  Active generation: %s\n", shortDigestOrNone(plan.ActiveGenerationID))
	fmt.Fprintf(stdout, "  Candidate generation: %s\n", shortDigest(plan.CandidateGenerationID))
	fmt.Fprintf(stdout, "  Restart required: %s\n", yesNo(plan.Impact.RestartRequired))
	fmt.Fprintf(stdout, "  Migration required: %s\n", yesNo(plan.Impact.MigrationRequired))
	fmt.Fprintf(stdout, "  Rollback compatible: %s\n", yesNo(plan.Impact.RollbackCompatible))
	return nil
}

func renderAppliedUpdate(raw string, stdout io.Writer) error {
	result, err := decodeMachineJSON[machineApplyResult](raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(result.PlanID) == "" {
		return errors.New("Loki appliance update apply output is invalid")
	}
	fmt.Fprintln(stdout, "Applied Loki update")
	fmt.Fprintf(stdout, "  Plan: %s\n", shortDigest(result.PlanID))
	if len(result.InterruptedJobs) == 0 {
		fmt.Fprintln(stdout, "  Interrupted jobs: none")
	} else {
		fmt.Fprintf(stdout, "  Interrupted jobs: %s\n", strings.Join(result.InterruptedJobs, ", "))
	}
	return nil
}

func renderBackup(raw string, stdout io.Writer) error {
	record, err := decodeMachineJSON[machineBackupRecord](raw)
	if err != nil {
		return err
	}
	if strings.TrimSpace(record.ID) == "" || record.CreatedAt.IsZero() {
		return errors.New("Loki appliance backup output is invalid")
	}
	fmt.Fprintln(stdout, "Created Loki backup")
	fmt.Fprintf(stdout, "  ID: %s\n", record.ID)
	fmt.Fprintf(stdout, "  Created: %s\n", record.CreatedAt.UTC().Format(time.RFC3339))
	if record.Installed != nil {
		fmt.Fprintf(stdout, "  Release: v%s\n", strings.TrimPrefix(record.Installed.Spec.Version, "v"))
	}
	return nil
}

func renderBooleanMutation(raw, field, successMessage string, stdout io.Writer) error {
	var payload map[string]json.RawMessage
	payload, err := decodeMachineJSON[map[string]json.RawMessage](raw)
	if err != nil {
		return err
	}
	if len(payload) != 1 {
		return errors.New("Loki appliance mutation output is invalid")
	}
	value, ok := payload[field]
	if !ok {
		return errors.New("Loki appliance mutation output is missing expected result")
	}
	var success bool
	if err := json.Unmarshal(value, &success); err != nil || !success {
		return errors.New("Loki appliance mutation did not report success")
	}
	fmt.Fprintln(stdout, successMessage)
	return nil
}

func renderOperatorStatus(
	status windowshost.OperatorStatus,
	binding windowshost.ReleaseBinding,
	distribution, applianceBase string,
	stdout io.Writer,
) {
	fmt.Fprintln(stdout, "Loki Windows")
	fmt.Fprintf(stdout, "  Distribution: %s\n", distribution)
	fmt.Fprintf(stdout, "  Frontend: %s\n", binding.ReleaseTag)
	fmt.Fprintf(stdout, "  Appliance base image: v%s\n", strings.TrimPrefix(applianceBase, "v"))
	if status.Release != "" {
		fmt.Fprintf(stdout, "  Appliance release: v%s\n", strings.TrimPrefix(status.Release, "v"))
	}
	fmt.Fprintf(stdout, "  Status: %s\n", status.State)
	switch {
	case status.UpdatePrepared:
		fmt.Fprintln(stdout, "  Update: prepared")
	case status.UpdateAvailable:
		fmt.Fprintln(stdout, "  Update: available")
	default:
		fmt.Fprintln(stdout, "  Update: none prepared")
	}
}

func generationVersion(generation *machineGeneration) string {
	if generation == nil {
		return "none"
	}
	version := strings.TrimSpace(generation.Spec.Version)
	if version == "" {
		return shortDigest(generation.ID)
	}
	return "v" + strings.TrimPrefix(version, "v")
}

func shortDigestOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "none"
	}
	return shortDigest(value)
}

func shortDigest(value string) string {
	value = strings.TrimSpace(value)
	const prefix = "sha256:"
	if strings.HasPrefix(value, prefix) && len(value) >= len(prefix)+12 {
		return prefix + value[len(prefix):len(prefix)+12]
	}
	return value
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
