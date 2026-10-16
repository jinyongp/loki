package main

import (
	"bytes"
	"context"
	"encoding/json"
	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	"loki/internal/rpc"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestFullSharingReadinessRequiresAnActualPublicOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loki.toml")
	for _, check := range []struct {
		config string
		ready  bool
	}{
		{"root = \"/workspace\"\n", false},
		{"root = \"/workspace\"\npublic_hosts = [\"mcp.example.com\"]\nartifact_base_url = \"https://mcp.example.com/artifacts\"\n", true},
	} {
		if err := os.WriteFile(path, []byte(check.config), 0600); err != nil {
			t.Fatal(err)
		}
		if err := sharingConfigReady(path); (err == nil) != check.ready {
			t.Fatalf("sharing readiness = %v, want ready %v", err, check.ready)
		}
	}
}

func TestFullRuntimeProbePreservesRecoveryDiagnostic(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "runtime.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	want := &fault.StoreRecovery{Schema: 1, Owner: "github", Reason: "missing_key"}
	server := rpc.Server{Principals: identity.UnixResolver{AgentUID: uint32(os.Getuid())}, Operations: map[string]rpc.Operation{"status": {Grant: controlpolicy.Agent, Handle: func(context.Context, json.RawMessage) (any, error) { return nil, want }}}}
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	var stderr bytes.Buffer
	if code := runFullProbe([]string{"--role", "runtime", "--socket", socket, "--expected-uid", strconv.Itoa(os.Getuid())}, &stderr); code != 1 {
		t.Fatalf("failed store probe exited %d", code)
	}
	got := fault.ParseStoreRecovery(stderr.Bytes())
	if got == nil || *got != *want || !strings.Contains(stderr.String(), "loki backups list") {
		t.Fatalf("probe hid actionable recovery: %s", stderr.String())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
