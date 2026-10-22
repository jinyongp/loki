//go:build linux

package secretbroker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/rpc"
)

func TestRealApplicationRoleSockets(t *testing.T) {
	broker, provider := os.Getenv("LOKI_SECRET_TEST_BROKER"), os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if broker == "" || provider == "" || os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_SECRET_ROLES") == "1" {
			t.Fatal("root and exact native broker/provider binaries are required")
		}
		t.Skip("native values Actions suite supplies root and exact binaries")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	root, err := os.MkdirTemp("/tmp", "loki-value-roles-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if os.Chmod(root, 0755) != nil {
		t.Fatal("fixture root permissions")
	}
	copyExecutable := func(source, name string) string {
		t.Helper()
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if os.WriteFile(path, data, 0555) != nil || os.Chmod(path, 0555) != nil {
			t.Fatal("fixture executable")
		}
		return path
	}
	broker = copyExecutable(broker, "broker-bin")
	provider = copyExecutable(provider, "provider-bin")
	makeDir := func(name string, mode os.FileMode, gid int) string {
		t.Helper()
		path := filepath.Join(root, name)
		if os.Mkdir(path, mode) != nil || os.Chown(path, 1000, gid) != nil || os.Chmod(path, mode) != nil {
			t.Fatal("fixture role directory")
		}
		return path
	}
	data := makeDir("provider", 0700, 1000)
	metadata := makeDir("metadata", 0700, 1000)
	jobs := filepath.Join(makeDir("jobs", 0700, 1000), "jobs.sock")
	adminSocket := filepath.Join(makeDir("admin", 0700, 1000), "admin.sock")
	metaSocket := filepath.Join(makeDir("agent", 0750, 10000), "metadata.sock")
	inbox, err := os.MkdirTemp("/dev/shm", "loki-role-inbox-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(inbox) })
	if os.Chown(inbox, 1000, 1000) != nil || os.Chmod(inbox, 0700) != nil {
		t.Fatal("fixture inbox")
	}
	resource := controlpolicy.ApplicationResource{ID: "c199846b-43ad-468c-9229-11976d4e5b3a", Alias: "demo", Repository: "github.com/fixture/app", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Write: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none"}
	layout := map[string]any{"uid": 1000, "launcher_uid": 0, "binary": provider, "root": data, "socket": jobs, "deployment_id": "1bbf75ed-1b51-46bd-8255-31536e945564", "policy_sha256": strings.Repeat("a", 64), "runner_id": "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", "instance_id": "a203906e-1c9c-4cc5-acb7-a68d118c54da", "resources": []controlpolicy.ApplicationResource{resource}, "metadata_socket": metaSocket, "metadata_gid": 10000, "agent_uid": 10000, "principal": "agent", "admin_socket": adminSocket, "application_root": metadata, "inbox": inbox}
	encoded, _ := json.Marshal(layout)
	layoutPath := filepath.Join(root, "layout.json")
	if os.WriteFile(layoutPath, encoded, 0644) != nil || os.Chmod(layoutPath, 0644) != nil {
		t.Fatal("fixture layout")
	}
	startBroker := func() func() {
		t.Helper()
		cmd := exec.CommandContext(ctx, broker, "--layout", layoutPath)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000, Groups: []uint32{10000}}}
		var diagnostics bytes.Buffer
		cmd.Stdout = &diagnostics
		cmd.Stderr = &diagnostics
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("broker shutdown: %v", err)
					}
				case <-time.After(3 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					t.Error("broker did not shut down")
				}
			})
		}
		t.Cleanup(stop)
		for {
			_, metaErr := os.Stat(metaSocket)
			_, adminErr := os.Stat(adminSocket)
			if metaErr == nil && adminErr == nil {
				break
			}
			select {
			case err := <-done:
				done <- err
				t.Fatalf("role broker did not start: %v %s", err, &diagnostics)
			case <-ctx.Done():
				t.Fatal("role broker readiness timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
		return stop
	}
	stopBroker := startBroker()
	client := admin.ApplicationClient{Socket: adminSocket, ExpectedUID: 1000}
	if _, err := client.Call(ctx, appvalues.AdminRequest{Action: "revision", Resource: "demo"}, nil); err != nil {
		t.Fatal("root administration was rejected", err)
	}
	uid := uint32(1000)
	if _, err := (rpc.Client{Socket: metaSocket, ExpectedUID: &uid}).Call(ctx, map[string]any{"operation": "inspect", "action": "resources"}); err == nil {
		t.Fatal("root used agent metadata socket")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaryBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "role-client")
	if os.WriteFile(helper, binaryBytes, 0555) != nil || os.Chmod(helper, 0555) != nil {
		t.Fatal("role helper binary")
	}
	for _, role := range []struct {
		uid  uint32
		mode string
	}{{10000, "agent"}, {10001, "foreign"}} {
		child := exec.CommandContext(ctx, helper, "-test.run=^TestApplicationRoleClient$")
		child.Env = []string{"PATH=/usr/bin:/bin", "LOKI_ROLE_MODE=" + role.mode, "LOKI_ROLE_METADATA=" + metaSocket, "LOKI_ROLE_ADMIN=" + adminSocket, "LOKI_ROLE_JOBS=" + jobs, "LOKI_ROLE_PROVIDER=" + data, "LOKI_ROLE_INBOX=" + inbox}
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: role.uid, Gid: role.uid, Groups: []uint32{10000}}}
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("%s role boundary failed: %v %s", role.mode, err, out)
		}
	}
	// Exercise a real process restart after a successful import consumed its
	// tmpfs input. Only durable metadata and the original provider receipt remain.
	providerCall := func(input string, args ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, provider, append(args, "--isolated-root", data)...)
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
		command.Stdin = strings.NewReader(input)
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000}}
		if err := command.Run(); err != nil {
			t.Fatalf("synthetic provider %s failed: %v", args[0], err)
		}
	}
	providerCall("synthetic-role-fixture-passphrase", "vault", "init", "--backend", "passphrase", "--passphrase-stdin")
	t.Cleanup(func() { providerCall("", "vault", "lock") })
	stageRaw, err := client.Call(ctx, appvalues.AdminRequest{Action: "stage", Resource: "demo", Principal: "agent"}, []byte("TOKEN=synthetic-private-restart-canary\n"))
	if err != nil {
		t.Fatal(err)
	}
	revisionRaw, err := client.Call(ctx, appvalues.AdminRequest{Action: "revision", Resource: "demo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stage struct {
		ID string `json:"stage_id"`
	}
	var revision struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(stageRaw, &stage) != nil || json.Unmarshal(revisionRaw, &revision) != nil || stage.ID == "" || len(revision.Revision) != 64 {
		t.Fatal("private administration metadata was invalid")
	}
	request, _ := json.Marshal(map[string]any{"operation": "write", "action": "import_staged", "resource": "demo", "stage_id": stage.ID, "expected_revision": revision.Revision, "request_id": "efb1e1c4-8acb-43c8-9ba6-756b20cc50c3"})
	write := func(replayed string) {
		t.Helper()
		child := exec.CommandContext(ctx, helper, "-test.run=^TestApplicationRoleClient$")
		child.Env = []string{"PATH=/usr/bin:/bin", "LOKI_ROLE_MODE=agent", "LOKI_ROLE_METADATA=" + metaSocket, "LOKI_ROLE_ADMIN=" + adminSocket, "LOKI_ROLE_JOBS=" + jobs, "LOKI_ROLE_PROVIDER=" + data, "LOKI_ROLE_INBOX=" + inbox, "LOKI_ROLE_WRITE=" + string(request), "LOKI_ROLE_REPLAYED=" + replayed}
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10000, Gid: 10000, Groups: []uint32{10000}}}
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("agent receipt replay failed: %v %s", err, out)
		}
	}
	write("false")
	if _, err := os.Stat(filepath.Join(inbox, stage.ID+".data")); !os.IsNotExist(err) {
		t.Fatal("staged plaintext survived import")
	}
	stopBroker()
	_ = startBroker()
	write("true")
}

func TestApplicationRoleClient(t *testing.T) {
	mode := os.Getenv("LOKI_ROLE_MODE")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	uid := uint32(1000)
	client := rpc.Client{Socket: os.Getenv("LOKI_ROLE_METADATA"), ExpectedUID: &uid}
	result, err := client.Call(ctx, map[string]any{"operation": "inspect", "action": "resources"})
	if mode == "foreign" {
		if err == nil {
			t.Fatal("foreign kernel UID admitted")
		}
		return
	}
	if err != nil || !bytes.Contains(result, []byte(`"alias":"demo"`)) {
		t.Fatal("configured agent could not inspect its resource", err)
	}
	if raw := os.Getenv("LOKI_ROLE_WRITE"); raw != "" {
		var request map[string]any
		if json.Unmarshal([]byte(raw), &request) != nil {
			t.Fatal("invalid fixture metadata")
		}
		out, err := client.Call(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Replayed bool `json:"replayed"`
			Count    int  `json:"count"`
		}
		if json.Unmarshal(out, &response) != nil || response.Replayed != (os.Getenv("LOKI_ROLE_REPLAYED") == "true") || response.Count != 1 || bytes.Contains(out, []byte("synthetic-private-restart-canary")) {
			t.Fatal("original receipt or value-free response was lost")
		}
	}
	for _, path := range []string{os.Getenv("LOKI_ROLE_PROVIDER"), os.Getenv("LOKI_ROLE_INBOX")} {
		if entries, err := os.ReadDir(path); err == nil {
			t.Fatalf("agent read private directory containing %d entries", len(entries))
		}
	}
	for _, socket := range []string{os.Getenv("LOKI_ROLE_ADMIN"), os.Getenv("LOKI_ROLE_JOBS")} {
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			t.Fatal("agent reached privileged socket")
		}
	}
	for _, request := range []map[string]any{
		{"operation": "inspect", "action": "resources", "profile": "platform"},
		{"operation": "inspect", "action": "resources", "env": nil},
		{"operation": "set_secret", "resource": "demo", "value": "unaccepted"},
	} {
		if _, err := client.Call(ctx, request); err == nil {
			t.Fatal("metadata authority expanded")
		}
	}
}
