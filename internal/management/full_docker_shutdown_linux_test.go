package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStopUnrecordedDeploymentOnlyRemovesVerifiedPreparation(t *testing.T) {
	for _, scenario := range []string{"prepare", "absent", "service", "foreign", "network", "daemon-error"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "docker")
			b := &DockerFullBackend{Store: Store{Root: filepath.Join(root, "management")}, Binary: binary, Socket: "/fixture/docker.sock"}
			r := DeploymentReservation{ID: "deployment-" + strings.Repeat("a", 32), PlanDigest: strings.Repeat("b", 64), Services: []string{"mcp"}}
			c := fullContainerInspection{Name: "/" + b.containerName(r, "prepare")}
			c.Config.Labels = map[string]string{fullOwnerLabel: b.Store.FullOwner(), fullDeploymentLabel: r.ID, fullPlanLabel: r.PlanDigest, fullServiceLabel: "prepare"}
			if scenario == "service" {
				c.Name = "/" + b.containerName(r, "mcp")
				c.Config.Labels[fullServiceLabel] = "mcp"
			}
			if scenario == "foreign" {
				c.Config.Labels[fullOwnerLabel] = "foreign"
			}
			write := func(name string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(name, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			raw, _ := json.Marshal(c)
			write(binary+".inspection", raw, 0600)
			write(binary+".ids", []byte(strings.Repeat("c", 64)+"\n"), 0600)
			if scenario == "absent" {
				write(binary+".removed", nil, 0600)
			}
			network := ""
			if scenario == "network" {
				network = strings.Repeat("d", 64)
			}
			write(binary+".networks", []byte(network), 0600)
			if scenario == "daemon-error" {
				write(binary+".fail", nil, 0600)
			}
			write(binary, []byte(`#!/bin/sh
set -eu
printf '%s %s\n' "$3" "$4" >> "$0.calls"
case "$3 $4" in
  'container ls') if test ! -f "$0.removed"; then cat "$0.ids"; fi ;;
  'container inspect') cat "$0.inspection" ;;
  'network ls') test ! -f "$0.fail"; cat "$0.networks" ;;
  'container stop') : ;;
  'container rm') touch "$0.removed" ;;
  *) exit 2 ;;
esac
`), 0700)
			err := b.Stop(t.Context(), r)
			wantSuccess := scenario == "prepare" || scenario == "absent"
			if (err == nil) != wantSuccess {
				t.Fatalf("stop result: %v", err)
			}
			calls, _ := os.ReadFile(binary + ".calls")
			mutated := strings.Contains(string(calls), "container stop") || strings.Contains(string(calls), "container rm")
			if mutated != (scenario == "prepare") {
				t.Fatalf("unexpected resource mutation: %s", calls)
			}
		})
	}
}
