package main

import (
	"os"
	"path/filepath"
	"testing"

	"loki/internal/daemon"
)

func TestMCPLayoutRequiresExplicitPeers(t *testing.T) {
	uid := uint32(1000)
	valid := mcpLayout{RuntimeSocket: "/run/runtime.sock", PortGuardSocket: "/run/ports.sock", BrowserSocket: "/run/browser.sock", ExecutionContract: "/usr/share/doc/loki/execution-contract.json", RuntimeUID: &uid, PortGuardUID: &uid, BrowserUID: &uid}
	if _, err := valid.options("token"); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*mcpLayout){func(l *mcpLayout) { l.RuntimeUID = nil }, func(l *mcpLayout) { l.PortGuardUID = nil }, func(l *mcpLayout) { l.BrowserUID = nil }, func(l *mcpLayout) { l.RuntimeSocket = "relative" }, func(l *mcpLayout) { l.RGPath = "relative" }, func(l *mcpLayout) { l.ExecutionContract = "relative" }} {
		layout := valid
		mutate(&layout)
		if _, err := layout.options("token"); err == nil {
			t.Fatal("invalid peer/resource configuration accepted")
		}
	}
}

func TestServiceLayoutsCannotInjectPolicyGeneration(t *testing.T) {
	for _, test := range []struct {
		name string
		out  any
	}{
		{name: "mcp", out: &mcpLayout{}},
		{name: "runtime", out: &runtimeLayout{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "layout.json")
			if err := os.WriteFile(path, []byte(`{"PolicyGeneration":{"schema":1,"sha256":"forged"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := daemon.ReadJSON(path, test.out); err == nil {
				t.Fatal("caller-controlled policy generation was accepted")
			}
		})
	}
}
