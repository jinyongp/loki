package windows

import (
	"strings"
	"testing"
)

func TestConnectionTaskMismatchReportsOnlyFieldNames(t *testing.T) {
	platform, _, expected := connectionTaskFixture(t)
	if err := platform.Create(t.Context(), expected); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"presence", "action_count", "executable", "arguments", "description",
		"run_level", "principal", "trigger_count", "logon_trigger", "execution_limit", "restart_count", "restart_interval"} {
		t.Run(field, func(t *testing.T) {
			probe := platform.probe
			probe.Actions = append([]StartupTaskAction(nil), probe.Actions...)
			const sensitive = "diagnostic-must-not-echo-this-value"
			switch field {
			case "presence":
				probe.Present = false
			case "action_count":
				probe.Actions = nil
			case "executable":
				probe.Actions[0].Executable = sensitive
			case "arguments":
				probe.Actions[0].Arguments = sensitive
			case "description":
				probe.Description = sensitive
			case "run_level":
				probe.RunLevel = sensitive
			case "principal":
				probe.UserID = sensitive
			case "trigger_count":
				probe.TriggerCount = 2
			case "logon_trigger":
				probe.LogonTrigger = false
			case "execution_limit":
				probe.ExecutionTimeTicks = 0
			case "restart_count":
				probe.RestartCount = 0
			case "restart_interval":
				probe.RestartIntervalTicks = 0
			}
			err := validateConnectionTaskProbe(probe, expected, platform.user)
			if err == nil || !strings.Contains(err.Error(), field) || strings.Contains(err.Error(), sensitive) {
				t.Fatalf("ownership mismatch was not safely diagnosed for %s", field)
			}
		})
	}
	probe := platform.probe
	probe.UserID = ""
	if err := validateConnectionTaskProbe(probe, expected, ""); err == nil {
		t.Fatal("missing expected and observed principals were treated as ownership")
	}
}
