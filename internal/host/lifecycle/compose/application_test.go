package compose

import (
	"strings"
	"testing"

	"loki/internal/host/lifecycle"
)

func TestApplicationPreparationRejectsForeignVolumeBeforeHelper(t *testing.T) {
	for _, kind := range []string{"owner", "options", "root"} {
		t.Run(kind, func(t *testing.T) {
			backend, runner, workspace := composeBackendFixture(t)
			generation := composeGeneration(t, "1.2.3", "b")
			if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{Scope: "user", Workspace: workspace}); err != nil {
				t.Fatal(err)
			}
			value := string(runner.applicationVolume)
			switch kind {
			case "owner":
				value = strings.Replace(value, "loki-tools-", "foreign-", 1)
			case "options":
				value = strings.Replace(value, `"Driver":"local"`, `"Driver":"local","Options":{"device":"/host-private","type":"none","o":"bind"}`, 1)
			case "root":
				value = strings.Replace(value, `/var/lib/docker/volumes/loki_application-state/_data`, `/`, 1)
			}
			runner.outputs[strings.Join([]string{"volume", "inspect", "loki_application-state"}, "\x00")] = []byte(value)
			if err := backend.Restart(t.Context()); err == nil {
				t.Fatal("foreign data source accepted")
			}
			for _, call := range runner.snapshot() {
				if len(call.args) > 0 && call.args[0] == "run" {
					t.Fatal("helper ran before ownership validation")
				}
			}
		})
	}
}
