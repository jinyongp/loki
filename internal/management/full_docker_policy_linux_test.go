package management

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/tools"
)

func TestFullContainerOwnershipRejectsForeignDeploymentAndUnrecognizedChildren(t *testing.T) {
	b := &DockerFullBackend{Store: Store{Root: t.TempDir()}}
	r := DeploymentReservation{ID: "deployment-" + strings.Repeat("a", 32), PlanDigest: strings.Repeat("b", 64), Services: []string{"mcp"}}
	current := fullContainerInspection{}
	current.Name = "/" + b.containerName(r, "mcp")
	current.Config.Labels = map[string]string{fullOwnerLabel: b.Store.FullOwner(), fullDeploymentLabel: r.ID, fullPlanLabel: r.PlanDigest, fullServiceLabel: "mcp"}
	if err := b.requireParent(r, current); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{fullOwnerLabel, fullDeploymentLabel, fullPlanLabel, fullServiceLabel} {
		t.Run(key, func(t *testing.T) {
			copy := current
			copy.Config.Labels = map[string]string{}
			for name, value := range current.Config.Labels {
				copy.Config.Labels[name] = value
			}
			copy.Config.Labels[key] = "foreign"
			if err := b.requireParent(r, copy); err == nil {
				t.Fatal("foreign or unrecognized service accepted for deletion")
			}
		})
	}
	current.Config.Labels = map[string]string{fullOwnerLabel: b.Store.FullOwner(), fullDeploymentLabel: r.ID}
	if err := b.requireParent(r, current); err == nil {
		t.Fatal("parent labels alone accepted an arbitrary child")
	}
	current.Config.Labels["io.loki.owner"] = "job"
	current.Config.Labels["io.loki.job.id"] = strings.Repeat("c", 32)
	current.Config.Labels["io.loki.policy.sha256"] = strings.Repeat("d", 64)
	current.Config.Labels["io.loki.sandbox.sha256"] = strings.Repeat("e", 64)
	current.Config.Labels["io.loki.resource.component"] = "workload"
	current.Name = "/loki-job-" + strings.Repeat("c", 32)
	if err := b.requireParent(r, current); err != nil {
		t.Fatal(err)
	}
	current.Config.Labels[fullDeploymentLabel] = "deployment-" + strings.Repeat("f", 32)
	if err := b.requireParent(r, current); err == nil {
		t.Fatal("another deployment's job accepted for deletion")
	}
}

func TestFullMountArgumentsPreserveCommasAsOneCSVField(t *testing.T) {
	mount := FullMount{Kind: "bind", Source: filepath.Join(t.TempDir(), "module,source"), Target: "/opt/loki/modules/browser", ReadOnly: true}
	argument, err := dockerMountArgument(mount)
	if err != nil {
		t.Fatal(err)
	}
	fields, err := csv.NewReader(strings.NewReader(argument)).Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields[1] != "source="+mount.Source || fields[3] != "readonly" {
		t.Fatalf("mount source changed through Docker CSV arguments: %q", argument)
	}
	if _, err := dockerMountArgument(FullMount{Kind: "volume", Source: "foreign/name", Target: "/workspace"}); err == nil {
		t.Fatal("non-owned volume argument accepted")
	}
}

func TestFullVolumesPreserveBootstrapOwnership(t *testing.T) {
	argument, err := dockerMountArgument(FullMount{Kind: "volume", Source: "loki-tools-fixture-data-workspace", Target: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	fields, err := csv.NewReader(strings.NewReader(argument)).Read()
	if err != nil || len(fields) != 4 || fields[3] != "volume-nocopy" {
		t.Fatalf("volume can replace bootstrap ownership: %q (%v)", argument, err)
	}
}

func TestFullProbesShareObservationAndOmitInactiveModules(t *testing.T) {
	store := lifecycleStore(t)
	plan, err := store.PlanFull()
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveDeployment(plan)
	if err != nil {
		t.Fatal(err)
	}
	backend := &readinessBackend{reservation: reservation, observation: FullObservation{Ready: false, State: "degraded", Services: map[string]string{"mcp": "ready", "workspace": "ready", "runtime": "ready", "github": "provider credentials unavailable"}}}
	probes, err := store.FullProbes(backend)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []tools.ID{"runtime-core", "workspace"} {
		if err := probes[id](context.Background(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if backend.observations != 1 {
		t.Fatal("doctor did not share a single observed deployment")
	}
	if probes["github"] != nil {
		t.Fatal("inactive provider received a fabricated readiness probe")
	}
}

type readinessBackend struct {
	reservation  DeploymentReservation
	observation  FullObservation
	observations int
}

func (b *readinessBackend) Observe(context.Context, DeploymentReservation) (FullObservation, error) {
	b.observations++
	return b.observation, nil
}
func (*readinessBackend) Prepare(context.Context, DeploymentReservation, FullTopology, FullLayouts) error {
	return nil
}
func (*readinessBackend) Start(context.Context, DeploymentReservation) error         { return nil }
func (*readinessBackend) Stop(context.Context, DeploymentReservation) error          { return nil }
func (*readinessBackend) ConfirmAbsent(context.Context, DeploymentReservation) error { return nil }

func TestDockerInspectionRejectsPrivilegeAndAdditionalMounts(t *testing.T) {
	b := &DockerFullBackend{Store: Store{Root: t.TempDir()}}
	r := DeploymentReservation{ID: "deployment-" + strings.Repeat("a", 32), PlanDigest: strings.Repeat("b", 64), Services: []string{"mcp"}}
	s := fullContainerSpec{Name: b.containerName(r, "mcp"), Service: "mcp", Image: "example.com/core@sha256:" + strings.Repeat("c", 64), User: "10000:10000", Groups: []string{"10001"}, Command: []string{"/worker", "mcp"}, Mounts: []FullMount{}, Networks: []string{}, Capabilities: []string{}, Memory: 256 << 20, PIDs: 128, Tmpfs: map[string]string{}}
	fixture := map[string]any{"Name": "/" + s.Name, "Config": map[string]any{"Image": s.Image, "User": s.User, "Entrypoint": []string{"/worker"}, "Cmd": []string{"mcp"}, "Labels": map[string]string{fullOwnerLabel: b.Store.FullOwner(), fullDeploymentLabel: r.ID, fullPlanLabel: r.PlanDigest, fullServiceLabel: "mcp", fullSpecLabel: s.digest()}}, "HostConfig": map[string]any{"ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "GroupAdd": s.Groups, "Memory": s.Memory, "PidsLimit": s.PIDs, "NetworkMode": "none", "Tmpfs": map[string]string{}}, "Mounts": []any{}}
	data, _ := json.Marshal(fixture)
	fixture["HostConfig"].(map[string]any)["Init"] = true
	data, _ = json.Marshal(fixture)
	var inspection fullContainerInspection
	if err := json.Unmarshal(data, &inspection); err != nil {
		t.Fatal(err)
	}
	if err := b.verifySpec(r, s, inspection); err != nil {
		t.Fatal(err)
	}
	inspection.HostConfig.Privileged = true
	if err := b.verifySpec(r, s, inspection); err == nil {
		t.Fatal("privileged existing service accepted")
	}
	inspection.HostConfig.Privileged = false
	inspection.HostConfig.GroupAdd = append(inspection.HostConfig.GroupAdd, "0")
	if err := b.verifySpec(r, s, inspection); err == nil {
		t.Fatal("unexpected administrator group accepted")
	}
}
