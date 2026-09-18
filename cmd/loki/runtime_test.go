package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeRejectsRelativeExecutionContractBeforeConfigLoad(t *testing.T) {
	layout := filepath.Join(t.TempDir(), "runtime.json")
	if err := os.WriteFile(layout, []byte(`{"ExecutionContract":"relative/execution-contract.json"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if code := runRuntime([]string{"--layout", layout, "--config", filepath.Join(t.TempDir(), "missing.toml")}, &stderr); code != 2 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "invalid runtime execution contract") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
