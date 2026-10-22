//go:build linux

package secretbroker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/tools"
)

// Exercise discovery through the actual agent process, not a root probe of
// the broker socket. The same UID must authorize each hidden/visible binding.
func verifyBrokerMCPDiscovery(t *testing.T, ctx context.Context, root, worker, socket string, resources []controlpolicy.ApplicationResource) {
	t.Helper()
	audit := filepath.Join(root, "mcp-agent")
	if os.Mkdir(audit, 0700) != nil || os.Chown(audit, 10000, 10000) != nil {
		t.Fatal("private agent fixture")
	}
	contract := os.Getenv("LOKI_SECRET_TEST_EXECUTION_CONTRACT")
	if contract == "" {
		contract = "/usr/share/doc/loki/container-execution-contract.json"
	}
	paths := map[string]any{
		"mcp-tools.json":     tools.Config{Schema: 1, Release: tools.Release, Host: tools.Host{Kind: "local"}, Mode: tools.Full, Tools: []tools.Selection{{ID: "secrets", Enabled: true}}},
		"mcp-resources.json": map[string]any{"resources": resources},
		"mcp-layout.json":    map[string]any{"Tools": []string{"secrets"}, "ToolsConfigPath": filepath.Join(root, "mcp-tools.json"), "ApplicationSocket": socket, "ApplicationUID": 1000, "ApplicationCatalog": filepath.Join(root, "mcp-resources.json"), "ExecutionContract": contract},
	}
	for name, value := range paths {
		body, err := json.Marshal(value)
		if err != nil || os.WriteFile(filepath.Join(root, name), body, 0644) != nil || os.Chmod(filepath.Join(root, name), 0644) != nil {
			t.Fatal("public agent fixture metadata", err)
		}
	}
	configuration := filepath.Join(root, "mcp.toml")
	token := filepath.Join(root, "mcp-token")
	if os.WriteFile(configuration, []byte(fmt.Sprintf("root = \"/workspace\"\nhost = \"127.0.0.1\"\nport = 18765\naudit_log = %q\n", filepath.Join(audit, "audit.jsonl"))), 0644) != nil || os.Chmod(configuration, 0644) != nil || os.WriteFile(token, []byte(strings.Repeat("t", 43)), 0644) != nil || os.Chmod(token, 0644) != nil {
		t.Fatal("agent fixture configuration")
	}
	command := exec.CommandContext(ctx, worker, "mcp", "--config", configuration, "--layout", filepath.Join(root, "mcp-layout.json"), "--token-file", token)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10000, Gid: 10000, Groups: []uint32{10000}}}
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var processErr error
	go func() { processErr = command.Wait(); close(done) }()
	defer func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	var failure []byte
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("agent fixture exited before discovery: %v: %s", processErr, diagnostic.String())
		default:
		}
		probe := exec.CommandContext(ctx, worker, "full-probe", "--role", "mcp", "--address", "http://127.0.0.1:18765/mcp", "--token-file", token)
		body, err := probe.CombinedOutput()
		if err == nil {
			return
		}
		failure = body
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("actual agent could not discover broker tools: %s", failure)
}
