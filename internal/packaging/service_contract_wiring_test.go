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
	composeRaw, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		"egress-proxy, --host, 0.0.0.0, --port, \"18766\", --execution-contract, " + containerContract,
		"browser-proxy, --port, \"18767\", --execution-contract, " + containerContract,
		"--downloads, /var/lib/loki/browser-downloads, --execution-contract, " + containerContract,
	} {
		if !strings.Contains(string(composeRaw), command) {
			t.Fatalf("Compose service does not bind execution contract: %s", command)
		}
	}
}
