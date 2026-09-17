package main

import "testing"

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
