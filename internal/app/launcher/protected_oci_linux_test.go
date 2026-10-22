//go:build linux

package launcher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
			testRealProtectedOCIJob(t, root, name == "lock_after_delivery", name == "application_network")
		})
	}
}

func testRealProtectedOCIJob(t *testing.T, root string, locked, application bool) {
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
	p, err := sandbox.NewPolicy(sandbox.PolicyOptions{
		DeploymentOwner: os.Getenv("LOKI_PROTECTED_OWNER"), DeploymentID: deployment,
		GenerationSHA256: digest, Image: image, Workspace: work, UID: 10000, GID: 10000,
		Environment: []string{"PATH=/usr/bin:/bin"}, MemoryBytes: 128 << 20, PIDs: 32, TmpfsBytes: 16 << 20,
		Gateway: sandbox.GatewayPolicyOptions{
			Image: image, Binary: "/opt/loki/bin/loki", ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
			EgressPolicy: "/usr/share/doc/loki/egress-policy.json", ProxyPort: 18766, MemoryBytes: 64 << 20, PIDs: 16, TmpfsBytes: 16 << 20,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	uid := uint32(0)
	engine, err := sandbox.NewEngine(sandbox.EngineOptions{Socket: "/var/run/docker.sock", ExpectedUID: &uid})
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "journal")
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
		_, err = l.startWorkload(ctx, jobs.Workload{ID: id, RequestID: normalized.RequestID, RequestSHA256: fp, Resource: normalized.Resource, Environment: normalized.Environment, CWD: normalized.CWD, Argv: normalized.Argv, Network: normalized.Network}, policy)
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
			if strings.Contains(r.Result.Output.Text, canary) || strings.Count(r.Result.Output.Text, "[REDACTED]") != 4 {
				t.Fatal("protected output not fully masked")
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
