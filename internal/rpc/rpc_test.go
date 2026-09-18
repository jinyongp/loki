package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
)

func TestDecodeRejectsUnknownAndTrailingOperationInput(t *testing.T) {
	type request struct {
		Profile string `json:"profile"`
	}
	value, err := Decode[request](json.RawMessage(`{"operation":"profile_create","profile":"web"}`))
	if err != nil || value.Profile != "web" {
		t.Fatalf("valid typed request = %#v, %v", value, err)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"operation":"profile_create","profile":"web","profiel":"typo"}`),
		json.RawMessage(`{"operation":"profile_create","profile":7}`),
		json.RawMessage(`{"operation":"profile_create","profile":"web"} {}`),
	} {
		if _, err = Decode[request](raw); err == nil {
			t.Fatalf("accepted invalid typed request: %s", raw)
		}
	}
}

func TestAgentUIDCannotElevateThroughProcessIdentity(t *testing.T) {
	s := Server{Principals: identity.UnixResolver{AgentUID: 1000}}
	for _, pid := range []int32{0, 1, 42, 9999} {
		if s.Authorized(Peer{PID: pid, UID: 1000}, controlpolicy.HostAdministration) {
			t.Fatalf("Agent UID elevated to host administration with pid %d", pid)
		}
	}
}

func TestPeerPolicy(t *testing.T) {
	s := Server{Principals: identity.UnixResolver{AgentUID: 1000}}
	if !s.Authorized(Peer{PID: 1, UID: 0}, controlpolicy.HostAdministration) ||
		!s.Authorized(Peer{PID: 1, UID: 0}, controlpolicy.Agent) ||
		!s.Authorized(Peer{PID: 42, UID: 1000}, controlpolicy.Agent) ||
		s.Authorized(Peer{PID: 42, UID: 1000}, controlpolicy.HostAdministration) ||
		s.Authorized(Peer{PID: 42, UID: 1001}, controlpolicy.Agent) {
		t.Fatal("principal grant policy failed")
	}
	if s.Authorized(Peer{PID: 1, UID: 0}, controlpolicy.Grant(0)) {
		t.Fatal("unset grant failed open")
	}
	if (&Server{}).Authorized(Peer{UID: 0}, controlpolicy.Agent) {
		t.Fatal("server without a principal resolver failed open")
	}
}

func TestSocketRoundtripBoundsAndSanitization(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "runtime.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer listener.Close()
	var mu sync.Mutex
	events := []Event{}
	s := Server{Principals: identity.UnixResolver{AgentUID: uint32(os.Getuid())}, Operations: map[string]Operation{
		"echo":    {Grant: controlpolicy.Agent, Handle: func(ctx context.Context, raw json.RawMessage) (any, error) { return Decode[map[string]any](raw) }},
		"failure": {Grant: controlpolicy.Agent, Handle: func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("synthetic-private-value") }},
		"panic":   {Grant: controlpolicy.Agent, Handle: func(context.Context, json.RawMessage) (any, error) { panic("private-panic") }},
		"large":   {Grant: controlpolicy.Agent, Handle: func(context.Context, json.RawMessage) (any, error) { return strings.Repeat("x", MaxBytes), nil }},
	}, Audit: func(e Event) { mu.Lock(); defer mu.Unlock(); events = append(events, e) }}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, listener) }()
	uid := uint32(os.Getuid())
	c := Client{Socket: socket, ExpectedUID: &uid}
	result, err := c.Call(ctx, map[string]any{"operation": "echo", "value": "hello", "profile": "fixture", "action_name": "web", "secret_value": "synthetic-hidden"})
	if err != nil || !strings.Contains(string(result), "hello") {
		t.Fatalf("roundtrip: %s %v", result, err)
	}
	for _, operation := range []string{"failure", "panic", "large", "missing"} {
		_, err := c.Call(ctx, map[string]any{"operation": operation})
		if err == nil {
			t.Fatalf("missing error %s", operation)
		}
		if strings.Contains(err.Error(), "private") {
			t.Fatalf("error leak %s", operation)
		}
	}
	if _, err := c.Call(ctx, map[string]any{"operation": "echo", "value": strings.Repeat("x", MaxBytes)}); err == nil {
		t.Fatal("oversized request allowed")
	}
	if _, err := readFrame(strings.NewReader(strings.Repeat("x", MaxBytes+1))); err == nil {
		t.Fatal("oversized frame allowed")
	}
	wrongUID := uid + 1
	c.ExpectedUID = &wrongUID
	if _, err := c.Call(ctx, map[string]string{"operation": "echo"}); err == nil {
		t.Fatal("untrusted socket peer allowed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) < 5 {
		t.Fatalf("missing audit events: %v", events)
	}
	encoded, _ := json.Marshal(events[0])
	if events[0].Profile == nil || *events[0].Profile != "fixture" || strings.Contains(string(encoded), "synthetic-hidden") {
		t.Fatalf("audit metadata: %s", encoded)
	}
}
