package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostpolicy "loki/internal/host/policy"
	"loki/modules/execution/jobs"
	"loki/modules/execution/jobs/remote"
)

// Uses the shipping native image and the production full backend's actual
// container/spec ownership and root maintenance transport. All data is owned
// by this fixture; no installed host or operational vault is inspected.
func TestRealManagedFullAdmission(t *testing.T) {
	if os.Getenv("LOKI_MANAGED_ADMISSION_PEER") == "1" {
		root := uint32(0)
		client, err := remote.New(remote.Options{Socket: "/run/loki/launcher/control.sock", ExpectedUID: &root, PolicySHA256: os.Getenv("LOKI_MANAGED_POLICY"), Timeout: 20 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		req, fp, err := jobs.NormalizeStartRequest(jobs.StartRequest{RequestID: os.Getenv("LOKI_MANAGED_REQUEST_ID"), CWD: ".", Argv: []string{"/bin/sh", "-c", "sleep 60"}})
		if err != nil {
			t.Fatal(err)
		}
		id, err := jobs.JobIDForRequestID(req.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Start(t.Context(), jobs.Workload{ID: id, RequestID: req.RequestID, RequestSHA256: fp, CWD: req.CWD, Argv: req.Argv, Network: req.Network})
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			state, err := client.Inspect(t.Context(), result.JobID)
			if err != nil {
				t.Fatal(err)
			}
			if state.State == jobs.StateRunning {
				break
			}
			if state.State == jobs.StateTerminal || time.Now().After(deadline) {
				t.Fatal("fixture Job did not become running", state)
			}
			time.Sleep(50 * time.Millisecond)
		}
		fmt.Fprintln(os.Stdout, "fixture-job-running")
		return
	}
	image := os.Getenv("LOKI_TEST_NATIVE_SECRET_IMAGE")
	if image == "" {
		if os.Getenv("LOKI_REQUIRE_NATIVE_SECRET_COMPOSE") == "1" {
			t.Fatal("native shipping-image fixture is required")
		}
		t.Skip("native package suite supplies an immutable shipping image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	store := Store{Root: t.TempDir()}
	b, err := NewDockerFullBackend(store, "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	identity := sha256.Sum256([]byte(store.Root))
	request := sha256.Sum256([]byte(store.Root + "/job"))
	r := DeploymentReservation{ID: "deployment-" + hex.EncodeToString(identity[:16]), PlanDigest: strings.Repeat("b", 64), Services: []string{"launcher"}}
	if err := b.Stop(ctx, r); err != nil {
		t.Fatal("unstarted reservation could not be retired by absence proof", err)
	}
	requestID := deploymentStopUUID(DeploymentReservation{ID: "deployment-" + hex.EncodeToString(request[:16])})
	root := store.Root
	layoutDir := filepath.Join(root, "layout")
	workspace := filepath.Join(root, "workspace")
	for _, path := range []string{layoutDir, workspace} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	volume := store.FullOwner() + "-data-launcher"
	socketVolume := store.FullOwner() + "-socket-launcher"
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		ids, _ := b.containerIDs(cleanup, r)
		for _, id := range ids {
			_, _ = b.command(cleanup, "cleaning fixture container", nil, "rm", "--force", id)
		}
		for _, name := range []string{volume, socketVolume} {
			if _, err := b.command(cleanup, "checking fixture volume", nil, "volume", "inspect", name); err == nil {
				if _, err := b.command(cleanup, "cleaning fixture volume", nil, "volume", "rm", name); err != nil {
					t.Error(err)
				}
			}
		}
	})
	mount := FullMount{Kind: "volume", Source: volume, Target: "/var/lib/loki/launcher"}
	if err := b.ownedVolume(ctx, mount); err != nil {
		t.Fatal(err)
	}
	statePath, err := b.volumePath(ctx, mount)
	if err != nil {
		t.Fatal(err)
	}
	copyID, err := b.command(ctx, "creating native image extraction fixture", nil, "create", image)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(copyID))
	defer b.command(context.Background(), "removing extraction fixture", nil, "rm", "--force", id)
	worker := filepath.Join(root, "worker")
	for source, target := range map[string]string{"/etc/loki/launcher.json": filepath.Join(root, "template.json"), "/opt/loki/bin/loki": worker} {
		if _, err := b.command(ctx, "extracting shipping input", nil, "cp", id+":"+source, target); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(worker, 0555); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "template.json"))
	if err != nil {
		t.Fatal(err)
	}
	var layout map[string]any
	if json.Unmarshal(raw, &layout) != nil {
		t.Fatal("invalid shipping launcher layout")
	}
	layout["StateDirectory"], layout["Workspace"], layout["ToolchainStore"] = statePath, workspace, "/var/lib/loki/toolchains"
	layout["Image"], layout["GatewayImage"] = image, image
	layout["DeploymentOwner"], layout["DeploymentID"] = store.FullOwner(), r.ID
	layout["Volumes"] = []map[string]string{{"path": statePath, "name": volume}}
	raw, _ = json.Marshal(layout)
	if err := os.WriteFile(filepath.Join(root, "input.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = b.command(ctx, "preparing owned launcher metadata", nil, "run", "--rm", "--network", "none", "--read-only", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE", "--entrypoint", "/bin/sh", "--mount", "type=volume,source="+volume+",target=/var/lib/loki/launcher,volume-nocopy", "--mount", "type=bind,source="+root+",target=/fixture", image, "-ec", "umask 077; install -d -o 0 -g 0 -m 0700 /var/lib/loki/launcher/journal; install -d -o 0 -g 10001 -m 0750 /var/lib/loki/launcher/socket; install -m 0640 -o 0 -g 10001 /fixture/input.json /fixture/layout/launcher.json; chown 10000:10001 /fixture/workspace; chmod 2770 /fixture/workspace; /opt/loki/bin/loki host maintenance status --offline --launcher-layout /fixture/layout/launcher.json --state-mount /var/lib/loki/launcher >/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	spec := fullContainerSpec{Name: b.containerName(r, "launcher"), Service: "launcher", Image: image, User: "0:0", Groups: []string{"10001"}, Capabilities: []string{"DAC_OVERRIDE"}, Memory: 512 << 20, PIDs: 128, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,mode=0700"}, Networks: []string{},
		Command: []string{"/opt/loki/bin/loki-launcher", "--layout", "/etc/loki/layout/launcher.json", "--config", "/etc/loki/loki.toml", "--execution-contract", "/usr/share/doc/loki/container-execution-contract.json"},
		Mounts:  []FullMount{mount, {Kind: "volume", Source: volume, Target: statePath}, {Kind: "bind", Source: layoutDir, Target: "/etc/loki/layout", ReadOnly: true}, {Kind: "bind", Source: worker, Target: "/opt/loki/bin/loki", ReadOnly: true}, {Kind: "bind", Source: workspace, Target: workspace, ReadOnly: true}, {Kind: "bind", Source: b.Socket, Target: "/run/docker.sock"}, {Kind: "volume", Source: volume, Target: "/run/loki/launcher"}},
	}
	// The public socket gets its own owned volume in production. Use a second
	// fixture volume here too, so mounting it cannot expose the Job journal.
	socketMount := FullMount{Kind: "volume", Source: socketVolume, Target: "/run/loki/launcher"}
	if err := b.ownedVolume(ctx, socketMount); err != nil {
		t.Fatal(err)
	}
	spec.Mounts[len(spec.Mounts)-1] = socketMount
	_, err = b.command(ctx, "preparing owned socket identity", nil, "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--user", "0:0", "--entrypoint", "/bin/sh", "--mount", "type=volume,source="+socketVolume+",target=/run/loki/launcher,volume-nocopy", image, "-ec", "chown 0:10001 /run/loki/launcher; chmod 0750 /run/loki/launcher")
	if err != nil {
		t.Fatal(err)
	}
	directory, err := b.deploymentDirectory(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(directory, "services.json"), fullDockerRecord{Schema: 1, Reservation: r, Worker: "/opt/loki/bin/loki", Specs: []fullContainerSpec{spec}, Order: []string{"launcher"}}); err != nil {
		t.Fatal(err)
	}
	if err := b.createContainer(ctx, r, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := b.command(ctx, "starting fixture launcher", nil, "container", "start", spec.Name); err != nil {
		t.Fatal(err)
	}
	if err := b.waitService(ctx, fullDockerRecord{Reservation: r, Worker: "/opt/loki/bin/loki"}, spec); err != nil {
		t.Fatal(err)
	}
	generation, _, _, err := hostpolicy.CompileFilesWithApplications("../../packaging/images/config/loki.toml", "", "../../packaging/images/config/execution-contract.json", "")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executable, 0555); err != nil {
		t.Fatal(err)
	}
	peer, err := b.command(ctx, "starting a real executor-owned Job", nil, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--user", "10004:10001", "--env", "LOKI_MANAGED_ADMISSION_PEER=1", "--env", "LOKI_MANAGED_REQUEST_ID="+requestID, "--env", "LOKI_MANAGED_POLICY="+generation.Digest(), "--entrypoint", "/fixture/tests", "--mount", "type=bind,source="+executable+",target=/fixture/tests,readonly", "--mount", "type=volume,source="+socketVolume+",target=/run/loki/launcher,volume-nocopy", image, "-test.run=^TestRealManagedFullAdmission$", "-test.timeout=30s")
	if err != nil {
		t.Fatal(err, string(peer))
	}
	if !strings.Contains(string(peer), "fixture-job-running") {
		t.Fatal("executor did not confirm a running Job")
	}
	if err := b.Stop(ctx, r); err == nil {
		t.Fatal("default stop interrupted an active Job")
	}
	inspection, err := b.inspectContainer(ctx, spec.Name)
	if err != nil || !inspection.State.Running {
		t.Fatal("busy rejection stopped the launcher", err)
	}
	if err := b.Stop(WithJobInterruption(ctx), r); err != nil {
		t.Fatal(err)
	}
	if err := b.ConfirmAbsent(ctx, r); err != nil {
		t.Fatal(err)
	}
	record, err := b.record(r)
	if err != nil || record.StopMaintenance == nil || !record.StopMaintenance.Interrupt {
		t.Fatal("explicit stop intent was not retained", err)
	}
}
