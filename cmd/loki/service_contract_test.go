package main

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestServiceProxyOverridesMustMatchExecutionContract(t *testing.T) {
	contract, err := filepath.Abs(filepath.Join("..", "..", "packaging", "go", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := runEgressProxy([]string{
		"--port", "43000", "--execution-contract", contract,
		"--policy", filepath.Join(t.TempDir(), "policy.json"),
		"--profile", "dependency-install", "--audit", filepath.Join(t.TempDir(), "audit.jsonl"),
	}, &stderr); code != 2 {
		t.Fatalf("egress mismatch exit = %d", code)
	}
	stderr.Reset()
	if code := runBrowserProxy([]string{
		"--port", "43000", "--execution-contract", contract,
		"--port-guard-socket", filepath.Join(t.TempDir(), "port.sock"), "--port-guard-uid", "0",
	}, &stderr); code != 2 {
		t.Fatalf("browser-proxy mismatch exit = %d", code)
	}
	stderr.Reset()
	if code := runBrowser([]string{
		"--agent-uid", "0", "--socket-gid", "0", "--execution-contract", contract,
		"--proxy", "http://127.0.0.1:43000",
	}, &stderr); code != 2 {
		t.Fatalf("browser mismatch exit = %d", code)
	}
}
