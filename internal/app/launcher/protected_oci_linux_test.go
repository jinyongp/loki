//go:build linux

package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	"loki/internal/platform/repository"
	"loki/internal/platform/sandbox"
	"loki/modules/execution/jobs"
)

// Run only inside the disposable root launcher fixture. The broker and its
// provider run as UID1000; project code runs as UID10000 in another container.
func TestRealProtectedOCIJob(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_PROTECTED_OCI") != "1" {
		t.Skip("protected OCI fixture not configured")
	}
	for i, name := range []string{"unlocked", "lock_after_delivery", "application_network"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(os.Getenv("LOKI_PROTECTED_FIXTURE_ROOT"), string(rune('0'+i)))
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatal(err)
			}
			testRealProtectedOCIJob(t, root, name == "lock_after_delivery", name == "application_network", "")
		})
	}
}

func TestRealProtectedOCIFailures(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_PROTECTED_OCI") != "1" {
		t.Skip("protected OCI fixture not configured")
	}
	for _, name := range []string{"broker_crash", "cancel", "timeout", "storage_full", "flood", "capacity", "approval_ack_loss"} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(os.Getenv("LOKI_PROTECTED_FIXTURE_ROOT"), name)
			if err := os.Mkdir(root, 0755); err != nil {
				t.Fatal(err)
			}
			testRealProtectedOCIJob(t, root, false, false, name)
		})
	}
}

func testRealProtectedOCIJob(t *testing.T, root string, locked, application bool, faultMode string) {
	if os.Geteuid() != 0 {
		t.Fatal("fixture launcher must be root")
	}
	image := os.Getenv("LOKI_TEST_DOCKER_IMAGE")
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	broker := os.Getenv("LOKI_TEST_SECRET_BROKER")
	if root == "" || image == "" || binary == "" || broker == "" {
		t.Fatal("incomplete required fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	work := filepath.Join(root, "workspace")
	private := filepath.Join(root, "broker")
	for _, dir := range []string{work, private} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if os.Chmod(work, 0777) != nil || os.Chown(private, 1000, 1000) != nil {
		t.Fatal("fixture ownership")
	}
	for _, dir := range []string{".git/objects", ".git/refs", "outside/.git/objects", "outside/.git/refs"} {
		if err := os.MkdirAll(filepath.Join(work, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"HEAD": "ref: refs/heads/main\n", "config": "[core]\nrepositoryformatversion = 0\n"} {
		if err := os.WriteFile(filepath.Join(work, ".git", name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(work, "outside", ".git", name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	repositories, err := repository.New(work)
	if err != nil {
		t.Fatal(err)
	}
	defer repositories.Close()
	repositoryID, err := repositories.Identity(".")
	if err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(private, "dvt")
	t.Cleanup(func() { retireOwnedFixtureSession(t, binary, vault) })
	call := func(input string, args ...string) map[string]any {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, append(args, "--isolated-root", vault)...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000}}
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.Output()
		defer clear(output)
		var envelope struct {
			OK   bool           `json:"ok"`
			Data map[string]any `json:"data"`
		}
		if err != nil || json.Unmarshal(output, &envelope) != nil || !envelope.OK {
			t.Fatal("synthetic provider operation failed", args[0])
		}
		return envelope.Data
	}
	call("synthetic-private-passphrase", "vault", "init", "--backend", "passphrase", "--passphrase-stdin")
	const resourceID = "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2"
	const profile = "app-9af6c2e6c1d441a3a6e99fb3c07c67d2"
	const canary = "loki-private-canary-3c46829e"
	revision := call("", "values", "revision", "--profile", profile)["revision"].(string)
	call(canary, "secret", "set", "TOKEN", "--stdin", "--profile", profile, "--revision", revision, "--request-id", "06cfc9b1-2bbf-42fb-81a1-0bd6ade00224")
	digest := strings.Repeat("a", 64)
	incarnation := sha256.Sum256([]byte(root))
	deployment := "deployment-" + hex.EncodeToString(incarnation[:16])
	revision = call("", "values", "revision", "--profile", profile)["revision"].(string)
	call("", "var", "set", "EXTRA", "--value", "public-extra", "--profile", profile, "--revision", revision, "--request-id", "068ae12b-0324-4b65-b303-e32225d7d208")
	revision = call("", "bundle", "list", "--profile", profile)["revision"].(string)
	call("", "bundle", "set", "fixture", "--key", "TOKEN", "--key", "EXTRA", "--profile", profile, "--revision", revision, "--request-id", "f44f174f-ec82-4b10-a3b3-1e3d904d790d")
	resource := controlpolicy.ApplicationResource{ID: resourceID, Alias: "fixture", Repository: repositoryID, Bundle: "fixture", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}}
	if application {
		resource.Network = "application"
		resource.Destinations = []string{"registry.npmjs.org:443"}
		data, err := os.ReadFile(os.Getenv("LOKI_PROTECTED_NETWORK_CHECK"))
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(work, "network-check")
		if os.WriteFile(path, data, 0755) != nil || os.Chmod(path, 0755) != nil {
			t.Fatal("network fixture binary")
		}
	}
	socket := filepath.Join(private, "jobs.sock")
	layout := map[string]any{"uid": 1000, "launcher_uid": 0, "binary": binary, "root": vault, "socket": socket, "deployment_id": deployment, "policy_sha256": digest, "runner_id": "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", "instance_id": "a203906e-1c9c-4cc5-acb7-a68d118c54da", "resources": []controlpolicy.ApplicationResource{resource}, "base_environment": map[string]string{"PATH": "/usr/bin:/bin"}}
	body, _ := json.Marshal(layout)
	layoutPath := filepath.Join(root, "layout.json")
	if os.WriteFile(layoutPath, body, 0644) != nil {
		t.Fatal("fixture layout")
	}
	command := exec.CommandContext(ctx, broker, "--layout", layoutPath)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000}}
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Signal(syscall.SIGTERM); _ = command.Wait() })
	for {
		if _, err := os.Lstat(socket); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("broker did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	p, err := protectedFixturePolicy(image, work, deployment, digest)
	if err != nil {
		t.Fatal(err)
	}
	uid := uint32(0)
	engine, err := sandbox.NewEngine(sandbox.EngineOptions{Socket: "/var/run/docker.sock", ExpectedUID: &uid})
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "journal")
	if faultMode == "storage_full" {
		journalPath = filepath.Join(os.Getenv("LOKI_PROTECTED_STORAGE_ROOT"), "journal")
	}
	if err := os.Mkdir(journalPath, 0700); err != nil {
		t.Fatal(err)
	}
	journal, err := jobs.OpenJournal(journalPath, jobs.JournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	l, err := newLifecycle(ctx, p, engine, nil, journal, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if err = l.configureProtected(&ProtectedOptions{Socket: socket, BrokerUID: 1000, Principal: "agent", DeploymentID: deployment, Resources: []controlpolicy.ApplicationResource{resource}, Repository: func(_ context.Context, cwd string) (string, error) { return repositories.Identity(cwd) }}); err != nil {
		t.Fatal(err)
	}
	start := func(request jobs.StartRequest, policy string) (string, error) {
		normalized, fp, err := jobs.NormalizeStartRequest(request)
		if err != nil {
			return "", err
		}
		id, _ := jobs.JobIDForRequestID(normalized.RequestID)
		_, err = l.startWorkload(ctx, jobs.Workload{ID: id, RequestID: normalized.RequestID, RequestSHA256: fp, Resource: normalized.Resource, Environment: normalized.Environment, CWD: normalized.CWD, Argv: normalized.Argv, Network: normalized.Network, TimeoutSeconds: normalized.TimeoutSeconds}, policy)
		return id, err
	}
	base := jobs.StartRequest{RequestID: "594513d7-1839-4b41-b1ca-51532e8b3069", Resource: "fixture", Argv: []string{"/bin/sh", "-c", `test -z "$PARENT_CANARY" && test "$(readlink /proc/self/fd/0)" = /dev/null || exit 99; printf '%s' "$TOKEN"; printf '%s' "$TOKEN" >&2; printf '%s' "$TOKEN" > /proc/1/fd/1; touch /workspace/ready; while ! test -f /workspace/finish; do sleep 0.05; done; printf '%s' "$TOKEN"; exit 7`}}
	base.RequestID = privateio.OperationID(deployment, resourceID, "job")
	if application {
		base.Network = jobs.NetworkApplication
		base.Argv[2] = `/workspace/network-check --port 18080 --allowed-authority registry.npmjs.org:443 --denied-authority github.com:443 --check-only || exit 98; ` + base.Argv[2]
	}
	for _, change := range []string{"resource", "env", "policy", "repository", "dependency-install"} {
		request := base
		policy := digest
		switch change {
		case "resource":
			request.Resource = "ungranted"
		case "env":
			request.Environment = "production"
		case "policy":
			policy = strings.Repeat("f", 64)
		case "repository":
			request.CWD = "outside"
		case "dependency-install":
			request.Network = jobs.NetworkDependencyInstall
		}
		if _, err := start(request, policy); err == nil {
			t.Fatal("ungranted protected request accepted", change)
		}
	}
	bad := base
	bad.RequestID = privateio.OperationID(deployment, resourceID, "bad-bundle")
	badID, err := start(bad, digest)
	if err != nil {
		t.Fatal(err)
	}
	l.mu.Lock()
	badJob := l.active[badID]
	l.mu.Unlock()
	for {
		r, _, _ := journal.Get(badID)
		if r.State == jobs.StateTerminal && r.Result != nil && r.Result.Cleanup != jobs.CleanupPending {
			if r.Result.Outcome != jobs.OutcomeLaunchFailed || r.Result.Cleanup != jobs.CleanupComplete {
				if badJob != nil {
					<-badJob.done
					t.Logf("pre-delivery failure: %v", badJob.err)
				}
				t.Fatal("expanded bundle did not fail before execution")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("expanded bundle did not terminate")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := os.Stat(filepath.Join(work, "ready")); !os.IsNotExist(err) {
		t.Fatal("expanded bundle executed project code")
	}
	revision = call("", "bundle", "list", "--profile", profile)["revision"].(string)
	call("", "bundle", "set", "fixture", "--key", "TOKEN", "--profile", profile, "--revision", revision, "--request-id", "d88605e3-b129-4c21-94b4-2c9c0c457322")
	if faultMode == "approval_ack_loss" {
		testProtectedApprovalACKLoss(t, ctx, l, journal, engine, p, base, digest, work, call)
		return
	}
	if faultMode == "timeout" {
		base.TimeoutSeconds = 3
	}
	if faultMode == "cancel" || faultMode == "capacity" {
		base.Argv[2] = "sleep 300 & " + base.Argv[2]
	}
	if faultMode == "flood" {
		base.Argv[2] = `yes "$TOKEN" | head -c 2097152; ` + base.Argv[2]
	}
	id, err := start(base, digest)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("missing Job identity")
	}
	for {
		if _, err := os.Stat(filepath.Join(work, "ready")); err == nil {
			break
		}
		r, _, _ := journal.Get(id)
		if r.State == jobs.StateTerminal {
			t.Fatal("protected Job failed before ready")
		}
		select {
		case <-ctx.Done():
			t.Fatal("protected Job not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	client := http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}}
	get := func(path string) []byte {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/loki-job-"+id+path, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	inspect := get("/json")
	if bytes.Contains(inspect, []byte(canary)) {
		t.Fatal("application value reached Docker metadata")
	}
	var metadata struct {
		LogPath string `json:"LogPath"`
		Host    struct {
			Log struct {
				Type string `json:"Type"`
			} `json:"LogConfig"`
		} `json:"HostConfig"`
	}
	if json.Unmarshal(inspect, &metadata) != nil || metadata.LogPath != "" || metadata.Host.Log.Type != "none" {
		t.Fatal("persistent Docker logging enabled")
	}
	if bytes.Contains(get("/logs?stdout=1&stderr=1"), []byte(canary)) {
		t.Fatal("daemon retained raw canary")
	}
	if faultMode == "launcher_crash" {
		for {
			r, _, _ := journal.Get(id)
			if r.State == jobs.StateRunning && r.Protected.Cursor > 0 {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("crash prefix unavailable")
			case <-time.After(10 * time.Millisecond):
			}
		}
		manifest := protectedCrashManifest{Root: root, Image: image, Deployment: deployment, Digest: digest, JobID: id, Request: base, Resource: resource, BrokerPID: command.Process.Pid}
		body, _ := json.Marshal(manifest)
		if err := os.WriteFile(filepath.Join(root, "recovery.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		<-ctx.Done()
		t.Fatal("launcher crash fixture was not killed")
	}
	if faultMode != "" && faultMode != "flood" {
		runProtectedFault(t, ctx, l, journal, engine, command, base, id, work, journalPath, faultMode, start)
		return
	}
	if locked {
		for {
			r, _, _ := journal.Get(id)
			if r.State == jobs.StateRunning {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("started fact not journaled")
			case <-time.After(10 * time.Millisecond):
			}
		}
		call("", "vault", "lock")
	}
	if os.WriteFile(filepath.Join(work, "finish"), nil, 0600) != nil {
		t.Fatal("release canary")
	}
	for {
		r, _, _ := journal.Get(id)
		if r.State == jobs.StateTerminal && r.Result != nil && r.Result.Cleanup != jobs.CleanupPending {
			if r.Result == nil || r.Result.ExitCode == nil || *r.Result.ExitCode != 7 || r.Result.Outcome != jobs.OutcomeExited || r.Result.Cleanup != jobs.CleanupComplete || r.Protected == nil || !r.Protected.Complete {
				if r.Result != nil {
					t.Logf("safe result: outcome=%s cleanup=%s exit=%v protected=%v complete=%v", r.Result.Outcome, r.Result.Cleanup, r.Result.ExitCode, r.Protected != nil, r.Protected != nil && r.Protected.Complete)
				}
				t.Fatal("protected result incomplete")
			}
			if strings.Contains(r.Result.Output.Text, canary) || faultMode != "flood" && strings.Count(r.Result.Output.Text, "[REDACTED]") != 4 {
				t.Fatal("protected output not fully masked")
			}
			if faultMode == "flood" && (!r.Protected.Truncated || len(r.Protected.Output) != journal.MaxOutputBytes()) {
				t.Fatal("flood output was not consumed with bounded prefix")
			}
			if application && !strings.Contains(r.Result.Output.Text, "egress-direct-denied=ok") {
				t.Fatal("application egress checks missing")
			}
			serialized, _ := json.Marshal(r)
			if bytes.Contains(serialized, []byte(canary)) {
				t.Fatal("journal retained canary")
			}
			if len(r.Protected.Reports) != 2 || r.Protected.Reports[0].Result == nil || !r.Protected.Reports[0].Result.Recorded || r.Protected.Reports[1].Result == nil || r.Protected.Reports[1].Result.Recorded == locked {
				t.Fatal("engine fact reports not preserved")
			}
			if !locked {
				item := call("", "values", "execution", "status", r.Protected.Prepared.ExecutionID, "--profile", profile)["item"].(map[string]any)
				if item["execution_state"] != "reported_finished" || item["exit_code"] != float64(7) {
					t.Fatal("provider did not record verified engine exit")
				}
			} else {
				call("synthetic-private-passphrase", "vault", "unlock", "--passphrase-stdin")
				// Commit a real provider report, then deliberately discard its
				// response. Recovery must replay its original UUID/revision.
				original := r.Protected.Reports[1].Intent
				connection, err := net.Dial("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				request := privateio.ReconcileJobRequest{Authority: *r.Protected.Authority, JobID: r.ID, InstanceRef: r.InstanceRef, Prepared: *r.Protected.Prepared, Action: "report", Fact: &original.Fact, Intent: &privateio.OperationIntent{RequestID: original.RequestID, Revision: original.Revision}}
				if privateio.WriteControl(connection, privateio.JobRequest{Reconcile: &request}) != nil {
					t.Fatal("report recovery request")
				}
				var intent privateio.OperationIntent
				if privateio.ReadControl(connection, &intent) != nil || intent.RequestID != original.RequestID || intent.Revision != original.Revision {
					t.Fatal("original report changed")
				}
				if privateio.WriteControl(connection, intent) != nil {
					t.Fatal("report commit")
				}
				connection.Close()
				for {
					item := call("", "values", "execution", "status", r.Protected.Prepared.ExecutionID, "--profile", profile)["item"].(map[string]any)
					if item["execution_state"] == "reported_finished" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("report response loss fixture did not commit")
					case <-time.After(10 * time.Millisecond):
					}
				}
				if err := l.reconcileProtectedReports(ctx, r); err != nil {
					t.Fatal(err)
				}
				recovered, _, _ := journal.Get(id)
				if recovered.Protected.Reports[1].Result == nil || !recovered.Protected.Reports[1].Result.Recorded || recovered.Protected.Reports[1].Intent.RequestID != original.RequestID || recovered.Protected.Reports[1].Intent.Revision != original.Revision {
					t.Fatal("report receipt recovery changed operation")
				}
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("protected Job did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func protectedFixturePolicy(image, work, deployment, digest string) (sandbox.Policy, error) {
	return sandbox.NewPolicy(sandbox.PolicyOptions{
		DeploymentOwner: os.Getenv("LOKI_PROTECTED_OWNER"), DeploymentID: deployment, GenerationSHA256: digest, Image: image, Workspace: work, UID: 10000, GID: 10000,
		Environment: []string{"PATH=/usr/bin:/bin"}, MemoryBytes: 128 << 20, PIDs: 32, TmpfsBytes: 16 << 20,
		Gateway: sandbox.GatewayPolicyOptions{Image: image, Binary: "/opt/loki/bin/loki", ExecutionContract: "/usr/share/doc/loki/execution-contract.json", EgressPolicy: "/usr/share/doc/loki/egress-policy.json", ProxyPort: 18766, MemoryBytes: 64 << 20, PIDs: 16, TmpfsBytes: 16 << 20},
	})
}

type protectedCrashManifest struct {
	Root, Image, Deployment, Digest, JobID string
	Request                                jobs.StartRequest
	Resource                               controlpolicy.ApplicationResource
	BrokerPID                              int
}

func TestProtectedLauncherCrashChild(t *testing.T) {
	root := os.Getenv("LOKI_PROTECTED_CRASH_CHILD_ROOT")
	if root == "" {
		t.Skip("separate crash child not selected")
	}
	testRealProtectedOCIJob(t, root, false, false, "launcher_crash")
}

func TestRealProtectedOCILauncherCrash(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "original"
		if replacement {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) { testProtectedOCILauncherCrash(t, replacement) })
	}
}

func testProtectedOCILauncherCrash(t *testing.T, replacement bool) {
	if os.Getenv("LOKI_REQUIRE_PROTECTED_OCI") != "1" {
		t.Skip("protected OCI fixture not configured")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	root := filepath.Join(os.Getenv("LOKI_PROTECTED_FIXTURE_ROOT"), "launcher_crash")
	if replacement {
		root += "_replacement"
	}
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestProtectedLauncherCrashChild$", "-test.timeout", "55s")
	child.Env = append(os.Environ(), "LOKI_PROTECTED_CRASH_CHILD_ROOT="+root)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	var manifest protectedCrashManifest
	for {
		raw, err := os.ReadFile(filepath.Join(root, "recovery.json"))
		if err == nil {
			if json.Unmarshal(raw, &manifest) != nil {
				t.Fatal("invalid crash metadata")
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("separate launcher not ready for SIGKILL")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Bind the broker before killing its launcher parent; never signal by name.
	brokerFD, err := unix.PidfdOpen(manifest.BrokerPID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(brokerFD)
	brokerArgs, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(manifest.BrokerPID), "cmdline"))
	expectedArgs := []byte(os.Getenv("LOKI_TEST_SECRET_BROKER") + "\x00--layout\x00" + filepath.Join(root, "layout.json") + "\x00")
	if err != nil || !bytes.Equal(brokerArgs, expectedArgs) {
		t.Fatal("crash fixture broker identity changed")
	}
	defer func() {
		_ = unix.PidfdSendSignal(brokerFD, syscall.SIGTERM, nil, 0)
		retireOwnedFixtureSession(t, os.Getenv("LOKI_SECRET_TEST_DEVTOOLS"), filepath.Join(root, "broker", "dvt"))
	}()
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("launcher was not killed")
	}
	journal, err := jobs.OpenJournal(filepath.Join(root, "journal"), jobs.JournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	before, ok, err := journal.Get(manifest.JobID)
	if err != nil || !ok || before.State != jobs.StateRunning {
		t.Fatal("crash did not preserve durable active Job", err)
	}
	uid := uint32(0)
	engine, err := sandbox.NewEngine(sandbox.EngineOptions{Socket: "/var/run/docker.sock", ExpectedUID: &uid})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := resourceFromRecord(before)
	if err != nil {
		t.Fatal(err)
	}
	state, err := engine.InspectJob(ctx, resource, before.InstanceRef)
	if err != nil || !state.Running {
		t.Fatal("control loss was mistaken for immediate workload termination", err)
	}
	work := filepath.Join(root, "workspace")
	policy, err := protectedFixturePolicy(manifest.Image, work, manifest.Deployment, manifest.Digest)
	if err != nil {
		t.Fatal(err)
	}
	l, err := newLifecycle(ctx, policy, engine, nil, journal, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer l.close()
	if err := l.configureProtected(&ProtectedOptions{Socket: filepath.Join(root, "broker", "jobs.sock"), BrokerUID: 1000, Principal: "agent", DeploymentID: manifest.Deployment, Resources: []controlpolicy.ApplicationResource{manifest.Resource}, Repository: func(context.Context, string) (string, error) { return manifest.Resource.Repository, nil }}); err != nil {
		t.Fatal(err)
	}
	if replacement {
		if _, err := engine.CleanupJob(ctx, resource, before.InstanceRef); err != nil {
			t.Fatal(err)
		}
		normalized, _, err := jobs.NormalizeStartRequest(manifest.Request)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := policy.ProtectedPlan(sandbox.WorkloadSpec{ID: manifest.JobID, PolicySHA256: manifest.Digest, CWD: normalized.CWD, Argv: normalized.Argv, Network: sandbox.NetworkNone})
		if err != nil {
			t.Fatal(err)
		}
		other, err := engine.PrepareProtectedJob(ctx, plan)
		if err != nil {
			t.Fatal(err)
		}
		if other.InstanceRef == before.InstanceRef {
			t.Fatal("replacement did not change actual identity")
		}
		if err := l.reconcile(); err == nil {
			t.Fatal("replacement did not fence admission")
		}
		state, err := engine.InspectJob(ctx, other.Resource, other.InstanceRef)
		if err != nil || !state.Exists {
			t.Fatal("recovery canceled the replacement", err)
		}
		if _, err := engine.CleanupJob(ctx, other.Resource, other.InstanceRef); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.reconcile(); err != nil {
		t.Fatal(err)
	}
	after, _, _ := journal.Get(manifest.JobID)
	if after.Result == nil || after.Result.Outcome != jobs.OutcomeOutputLost || after.Result.ExitCode != nil || after.Result.Cleanup != jobs.CleanupComplete || after.Protected.Gap == nil || after.Protected.Gap.Reason != "recovery_interrupted" || !bytes.Equal(before.Protected.Output, after.Protected.Output) || *before.Protected.Prepared != *after.Protected.Prepared {
		t.Fatal("launcher crash recovery changed evidence or resumed delivery")
	}
	normalized, fp, err := jobs.NormalizeStartRequest(manifest.Request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := l.startWorkload(ctx, jobs.Workload{ID: manifest.JobID, RequestID: normalized.RequestID, RequestSHA256: fp, Resource: normalized.Resource, Environment: normalized.Environment, CWD: normalized.CWD, Argv: normalized.Argv, Network: normalized.Network}, manifest.Digest)
	if err != nil || !result.Replayed {
		t.Fatal("crashed Job did not replay original request", err)
	}
	state, err = engine.InspectJob(ctx, resource, before.InstanceRef)
	if err != nil || state.Exists {
		t.Fatal("launcher recovery left or recreated OCI resource", err)
	}
}

// The passphrase session intentionally outlives ordinary provider CLI calls.
// Retire only this case's exact daemon in the disposable PID namespace, using
// pidfd so PID reuse can never signal another fixture or host process.
func testProtectedApprovalACKLoss(t *testing.T, ctx context.Context, l *lifecycle, j *jobs.Journal, engine *sandbox.Engine, policy sandbox.Policy, base jobs.StartRequest, digest, work string, call func(string, ...string) map[string]any) {
	t.Helper()
	request, fp, err := jobs.NormalizeStartRequest(base)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := jobs.JobIDForRequestID(request.RequestID)
	plan, err := policy.ProtectedPlan(sandbox.WorkloadSpec{ID: id, PolicySHA256: digest, CWD: request.CWD, Argv: request.Argv, Network: sandbox.NetworkNone})
	if err != nil {
		t.Fatal(err)
	}
	open, err := l.protectedSelection(ctx, jobs.Workload{ID: id, RequestID: request.RequestID, RequestSHA256: fp, Resource: request.Resource, Environment: request.Environment, CWD: request.CWD, Argv: request.Argv, Network: request.Network}, digest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := j.AdmitProtectedRequest(id, backendReference(plan), request.RequestID, fp, request.Network, nil, nil, open.DeploymentID, request.Resource, request.Environment, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if err := j.BindProtectedAuthority(id, l.protectedAuthority(*open), now); err != nil {
		t.Fatal(err)
	}
	prepared, err := engine.PrepareProtectedJob(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.BindInstance(id, prepared.InstanceRef, now); err != nil {
		t.Fatal(err)
	}
	attachment, err := engine.AttachProtectedJob(ctx, prepared.Resource, prepared.InstanceRef)
	if err != nil {
		t.Fatal(err)
	}
	defer attachment.Close()
	open.InstanceRef = prepared.InstanceRef
	open.BaseEnvironment, err = attachment.BootstrapEnvironment(plan)
	if err != nil {
		t.Fatal(err)
	}
	session, err := privateio.OpenJob(ctx, l.protected.Socket, l.protected.BrokerUID, *open, func(input privateio.PreparedJob) error {
		if _, err := j.BindProtectedInput(id, input, time.Now().UTC()); err != nil {
			return err
		}
		if input.Stage == "approved" {
			return errors.New("fixture dropped approval ACK")
		}
		return nil
	})
	if err == nil {
		session.Close()
		t.Fatal("approval ACK was not lost")
	}
	r, _, _ := j.Get(id)
	if r.Protected.Prepared == nil || r.Protected.Prepared.Stage != "approved" {
		t.Fatal("original mapping not durable before ACK")
	}
	cleanup, err := engine.CleanupJob(ctx, prepared.Resource, prepared.InstanceRef)
	if err != nil {
		t.Fatal(err)
	}
	r, err = j.MarkTerminal(id, jobs.Result{Outcome: jobs.OutcomeLaunchFailed, Cleanup: terminalCleanup(cleanup, err)}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := l.reconcileProtectedReports(ctx, r); err != nil {
		t.Fatal(err)
	}
	item := call("", "values", "execution", "status", r.Protected.Prepared.ExecutionID, "--profile", "app-9af6c2e6c1d441a3a6e99fb3c07c67d2")["item"].(map[string]any)
	if item["delivery_state"] != "canceled" {
		t.Fatal("pending frozen input not canceled")
	}
	workload := jobs.Workload{ID: id, RequestID: request.RequestID, RequestSHA256: fp, Resource: request.Resource, Environment: request.Environment, CWD: request.CWD, Argv: request.Argv, Network: request.Network}
	result, err := l.startWorkload(ctx, workload, digest)
	if err != nil || !result.Replayed {
		t.Fatal("original request was not replayed", err)
	}
	if _, err := os.Stat(filepath.Join(work, "ready")); !os.IsNotExist(err) {
		t.Fatal("lost ACK executed project code")
	}
	state, err := engine.InspectJob(ctx, prepared.Resource, prepared.InstanceRef)
	if err != nil || state.Exists {
		t.Fatal("lost ACK created a replacement OCI resource", err)
	}
}

func retireOwnedFixtureSession(t *testing.T, binary, root string) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Error(err)
		return
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			continue
		}
		cmdline, readErr := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		args := bytes.Split(bytes.TrimSuffix(cmdline, []byte{0}), []byte{0})
		info, statErr := os.Stat(filepath.Join("/proc", entry.Name()))
		owned := false
		if readErr == nil && statErr == nil && len(args) == 3 && string(args[0]) == binary && string(args[1]) == "__vault-session" {
			rel, err := filepath.Rel(root, string(args[2]))
			st, ok := info.Sys().(*syscall.Stat_t)
			owned = err == nil && rel != ".." && !strings.HasPrefix(rel, "../") && ok && st.Uid == 1000
		}
		if owned {
			if err := unix.PidfdSendSignal(fd, syscall.SIGTERM, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Error(err)
			}
		}
		unix.Close(fd)
	}
}

func runProtectedFault(t *testing.T, ctx context.Context, l *lifecycle, journal *jobs.Journal, engine *sandbox.Engine, broker *exec.Cmd, base jobs.StartRequest, id, work, journalPath, mode string, start func(jobs.StartRequest, string) (string, error)) {
	t.Helper()
	var before jobs.Record
	for {
		before, _, _ = journal.Get(id)
		if before.State == jobs.StateRunning && before.Protected.Cursor > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("durable protected prefix unavailable")
		case <-time.After(10 * time.Millisecond):
		}
	}
	ids := []string{id}
	if mode == "capacity" {
		for i := 1; i < 8; i++ {
			request := base
			request.RequestID = privateio.OperationID(before.Protected.DeploymentID, id, "capacity-"+string(rune('0'+i)))
			other, err := start(request, before.Protected.Authority.PolicySHA256)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, other)
		}
		for _, jobID := range ids {
			for {
				r, _, _ := journal.Get(jobID)
				if r.State == jobs.StateRunning {
					break
				}
				if r.State == jobs.StateTerminal {
					t.Fatal("capacity8 job failed")
				}
				select {
				case <-ctx.Done():
					t.Fatal("capacity8 not running")
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
		request := base
		request.RequestID = privateio.OperationID(before.Protected.DeploymentID, id, "capacity-9")
		if _, err := start(request, before.Protected.Authority.PolicySHA256); err == nil {
			t.Fatal("ninth protected Job admitted")
		}
		for _, jobID := range ids {
			if _, err := l.cancelStatus(ctx, jobID); err != nil {
				t.Fatal(err)
			}
		}
	} else if mode == "broker_crash" {
		if err := broker.Process.Signal(syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
	} else if mode == "cancel" {
		if _, err := l.cancelStatus(ctx, id); err != nil {
			t.Fatal(err)
		}
	} else if mode == "storage_full" {
		filler := filepath.Join(filepath.Dir(journalPath), "owned-filler")
		err := os.WriteFile(filler, make([]byte, 1<<20), 0600)
		if !errors.Is(err, syscall.ENOSPC) {
			t.Fatal("disposable tmpfs did not reach ENOSPC", err)
		}
		defer os.Remove(filler)
		if os.WriteFile(filepath.Join(work, "finish"), nil, 0600) != nil {
			t.Fatal("release storage canary")
		}
		job, ok := l.lookupActive(id)
		if ok {
			select {
			case <-job.done:
			case <-ctx.Done():
				t.Fatal("storage failure left Job alive")
			}
		}
		after, _, _ := journal.Get(id)
		if !bytes.Equal(after.Protected.Output, before.Protected.Output) || after.Protected.Complete {
			t.Fatal("disk full changed durable prefix or invented EOF")
		}
		if err := os.Remove(filler); err != nil {
			t.Fatal(err)
		}
		if err := l.reconcile(); err != nil {
			t.Fatal(err)
		}
	}
	for _, jobID := range ids {
		result, err := l.wait(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		r, _, _ := journal.Get(jobID)
		expected := jobs.OutcomeOutputLost
		if mode == "cancel" || mode == "capacity" {
			expected = jobs.OutcomeCanceled
		}
		if mode == "timeout" {
			expected = jobs.OutcomeTimedOut
		}
		if result.Outcome != expected || result.Cleanup != jobs.CleanupComplete || r.Protected.Gap == nil || r.Protected.Complete || r.Protected.Gap.Cursor != r.Protected.Cursor {
			t.Fatalf("fault %s misreported: outcome=%s cleanup=%s gap=%v", mode, result.Outcome, result.Cleanup, r.Protected.Gap)
		}
		resource, err := resourceFromRecord(r)
		if err != nil {
			t.Fatal(err)
		}
		state, err := engine.InspectJob(ctx, resource, r.InstanceRef)
		if err != nil || state.Exists {
			t.Fatal("fault left exact OCI resource", err)
		}
		if bytes.Contains(r.Protected.Output, []byte("loki-private-canary-3c46829e")) {
			t.Fatal("fault retained raw value")
		}
	}
}
