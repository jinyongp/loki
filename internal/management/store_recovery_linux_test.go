package management

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/fault"
)

func TestRuntimeRecoveryProbeUsesVerifiedCurrentContainer(t *testing.T) {
	dir := t.TempDir()
	b := &DockerFullBackend{Store: Store{Root: t.TempDir()}, Binary: filepath.Join(dir, "docker"), Socket: "/var/run/fixture.sock", Diagnostics: io.Discard}
	r := DeploymentReservation{ID: "deployment-" + strings.Repeat("a", 32), PlanDigest: strings.Repeat("b", 64), Services: []string{"runtime"}}
	s := fullContainerSpec{Name: b.containerName(r, "runtime"), Service: "runtime", Image: "example.com/core@sha256:" + strings.Repeat("c", 64), User: "0:10001", Groups: []string{"10001"}, Command: []string{"/worker", "runtime"}, Mounts: []FullMount{}, Networks: []string{}, Capabilities: []string{}, Memory: 256 << 20, PIDs: 128, Tmpfs: map[string]string{}}
	fixture := map[string]any{"ID": strings.Repeat("d", 64), "Name": "/" + s.Name, "Config": map[string]any{"Image": s.Image, "User": s.User, "Entrypoint": []string{"/worker"}, "Cmd": []string{"runtime"}, "Labels": map[string]string{fullOwnerLabel: b.Store.FullOwner(), fullDeploymentLabel: r.ID, fullPlanLabel: r.PlanDigest, fullServiceLabel: "runtime", fullSpecLabel: s.digest()}}, "State": map[string]any{"Running": false, "Status": "exited", "StartedAt": "2026-10-16T04:00:00.123456789Z"}, "HostConfig": map[string]any{"Init": true, "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "GroupAdd": s.Groups, "Memory": s.Memory, "PidsLimit": s.PIDs, "NetworkMode": "none", "Tmpfs": map[string]string{}}, "Mounts": []any{}}
	inspectionFile := filepath.Join(dir, "inspection.json")
	calls := filepath.Join(dir, "calls")
	t.Setenv("LOKI_RECOVERY_INSPECTION", inspectionFile)
	t.Setenv("LOKI_RECOVERY_COMMANDS", calls)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LOKI_RECOVERY_COMMANDS"
case "$*" in
 *"container inspect"*) cat "$LOKI_RECOVERY_INSPECTION" ;;
 *"container logs"*) printf 'private-token-not-for-users'; printf 'LOKI_STORE_RECOVERY {"schema":1,"owner":"secrets","reason":"missing_data"}\n' >&2 ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(b.Binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	save := func() {
		t.Helper()
		data, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(inspectionFile, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	record := fullDockerRecord{Reservation: r}
	err := b.probeService(t.Context(), record, s, "")
	var recovery *fault.StoreRecovery
	if !errors.As(err, &recovery) || recovery.Reason != "missing_data" || strings.Contains(err.Error(), "private-token") {
		t.Fatalf("missing safe recovery explanation: %v", err)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "--tail 64 --since 2026-10-16T04:00:00.123456789Z "+strings.Repeat("d", 64)) {
		t.Fatalf("diagnostics were not limited to current owned attempt: %s", data)
	}
	for _, kind := range []string{"foreign", "unknown-start"} {
		t.Run(kind, func(t *testing.T) {
			fixture["State"].(map[string]any)["StartedAt"] = "2026-10-16T04:00:00.123456789Z"
			fixture["Config"].(map[string]any)["Labels"].(map[string]string)[fullOwnerLabel] = b.Store.FullOwner()
			if kind == "foreign" {
				fixture["Config"].(map[string]any)["Labels"].(map[string]string)[fullOwnerLabel] = "foreign"
			} else {
				fixture["State"].(map[string]any)["StartedAt"] = ""
			}
			save()
			if err := os.WriteFile(calls, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if err := b.probeService(t.Context(), record, s, ""); err == nil {
				t.Fatal("accepted failed/foreign service")
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "container logs") {
				t.Fatal("read logs without ownership/current-attempt evidence")
			}
		})
	}
}
