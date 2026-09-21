package packaging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServicePortPolicyUsesExecutionContractInputs(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	nativeContract := "/usr/share/doc/loki/execution-contract.json"
	for _, relative := range []string{
		"packaging/go/systemd/loki-go-port-guard.service",
		"packaging/go/systemd/loki-go-egress-proxy.service",
		"packaging/go/systemd/loki-go-browser-proxy.service",
		"packaging/go/systemd/loki-go-browser.service",
		"packaging/go/systemd/loki-go-launcher.service",
		"packaging/go/systemd/loki-go-executor.service",
	} {
		raw, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "--execution-contract "+nativeContract) {
			t.Fatalf("%s does not bind the native execution contract", relative)
		}
	}
	nativeLayout, err := os.ReadFile(filepath.Join(root, "packaging/go/mcp.json.in"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(nativeLayout), `"ExecutionContract": "`+nativeContract+`"`) {
		t.Fatal("native MCP layout does not bind the execution contract")
	}
	if !strings.Contains(string(nativeLayout), `"PackagedSkillRoot": "/opt/loki/share/skills"`) {
		t.Fatal("native MCP layout does not bind the packaged Skill root")
	}
	if !strings.Contains(string(nativeLayout), `"ExecutorSocket": "/run/loki-go/executor/control.sock"`) {
		t.Fatal("native MCP layout does not bind the executor socket")
	}
	if !strings.Contains(string(nativeLayout), `"ToolchainStore": "/var/lib/loki-go/toolchains"`) ||
		!strings.Contains(string(nativeLayout), `"ToolchainCatalog": "/usr/share/doc/loki/toolchain-catalog.json"`) {
		t.Fatal("native MCP layout does not bind managed toolchain inputs")
	}
	launcherLayout, err := os.ReadFile(filepath.Join(root, "packaging/go/launcher.json.in"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(launcherLayout), `"ToolchainStore": "/var/lib/loki-go/toolchains"`) {
		t.Fatal("native launcher layout does not bind the managed toolchain store")
	}
	lifecycle, err := os.ReadFile(filepath.Join(root, "scripts/loki-go-lifecycle.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"toolchain provision-managed",
		"--catalog \"$release/usr/share/doc/loki/toolchain-catalog.json\"",
		"--store \"$(rooted /var/lib/loki-go/toolchains)\"",
	} {
		if !strings.Contains(string(lifecycle), want) {
			t.Fatalf("native lifecycle does not provision managed toolchains: %s", want)
		}
	}
	containerContract := "/usr/share/doc/loki/container-execution-contract.json"
	var containerLayout map[string]any
	raw, err := os.ReadFile(filepath.Join(root, "packaging/container/config/mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &containerLayout); err != nil {
		t.Fatal(err)
	}
	if containerLayout["ExecutionContract"] != containerContract {
		t.Fatalf("container MCP execution contract = %#v", containerLayout["ExecutionContract"])
	}
	if containerLayout["PackagedSkillRoot"] != "/opt/loki/share/skills" {
		t.Fatalf("container MCP packaged Skill root = %#v", containerLayout["PackagedSkillRoot"])
	}
	if containerLayout["ExecutorSocket"] != "/run/loki/executor/control.sock" {
		t.Fatalf("container MCP executor socket = %#v", containerLayout["ExecutorSocket"])
	}
	composeRaw, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"egress-proxy, --host, 0.0.0.0, --port, \"18766\", --execution-contract, " + containerContract,
		"browser-proxy, --port, \"18767\", --execution-contract, " + containerContract,
		"--downloads, /var/lib/loki/browser-downloads, --config, /etc/loki/loki.toml, --execution-contract, " + containerContract,
		"--layout, /etc/loki/launcher.json, --execution-contract, " + containerContract,
		"--layout, /etc/loki/executor.json, --execution-contract, " + containerContract,
		"user-skills:/var/lib/loki/runner/agents",
		"user-skills:/home/runner/.agents:ro",
		"launcher-socket:/run/loki/launcher",
		"executor-socket:/run/loki/executor",
	} {
		if !strings.Contains(string(composeRaw), command) {
			t.Fatalf("Compose service does not bind execution contract: %s", command)
		}
	}
}
