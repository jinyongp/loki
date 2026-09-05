package action

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/process"
	"loki/internal/secret"
)

func testPorts() *portRegistry { return &portRegistry{held: make(map[int]*portLease), now: time.Now} }

func TestPortReservationConcurrencyAndExpiry(t *testing.T) {
	p := testPorts()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	busy := listener.Addr().(*net.TCPAddr).Port
	var group sync.WaitGroup
	leases := make(chan *portLease, 32)
	for range 32 {
		group.Go(func() {
			lease, err := p.allocate(busy)
			if err != nil {
				t.Error(err)
				return
			}
			leases <- lease
		})
	}
	group.Wait()
	close(leases)
	seen := map[int]bool{}
	for lease := range leases {
		if lease.port == busy || seen[lease.port] {
			t.Fatal("busy or duplicate development port allocated")
		}
		seen[lease.port] = true
		p.release(lease)
	}
	if len(seen) != 32 || len(p.held) != 0 {
		t.Fatal("reservation count/cleanup")
	}
	first, err := p.allocate(0)
	if err != nil {
		t.Fatal(err)
	}
	now := first.expires
	p.now = func() time.Time { return now }
	second, err := p.allocate(first.port)
	if err != nil {
		t.Fatal(err)
	}
	if second.port != first.port {
		t.Fatal("expired preferred reservation was retained")
	}
	p.release(first)
	if p.held[second.port] != second {
		t.Fatal("stale reservation removed a new lease")
	}
	p.release(second)
}

func TestLaunchTokenScopeOneShotAndBounds(t *testing.T) {
	p := testPorts()
	r := &Runtime{ports: p, launches: map[string]preparedAction{}}
	plan := secret.ActionPlan{Profile: "fixture", Action: "web", CWD: "/synthetic/repo", Policy: secret.ActionPolicy{DynamicPort: &secret.DynamicPort{}}}
	for _, mismatch := range []string{"profile", "action", "cwd", "expired", "valid"} {
		token, lease, err := r.reserve(plan)
		if err != nil {
			t.Fatal(err)
		}
		if !launchTokenPattern.MatchString(token) {
			t.Fatal("token format")
		}
		attempt := plan
		switch mismatch {
		case "profile":
			attempt.Profile = "other"
		case "action":
			attempt.Action = "other"
		case "cwd":
			attempt.CWD += "-worktree"
		case "expired":
			p.now = func() time.Time { return lease.expires }
		}
		got, err := r.consume(token, attempt)
		if mismatch == "valid" {
			if err != nil || got != lease {
				t.Fatalf("valid token: %v", err)
			}
			p.release(got)
		} else if err == nil {
			t.Fatal("invalid token scope accepted")
		}
		if _, err = r.consume(token, plan); err == nil || err.Error() != "action launch token is unknown or expired" {
			t.Fatalf("token was not one-shot: %v", err)
		}
		p.now = time.Now
	}
	for _, token := range []string{"", "not-a-token", strings.Repeat("A", 32), strings.Repeat("0", 33)} {
		if _, err := r.consume(token, plan); err == nil || err.Error() != "invalid action launch token" {
			t.Fatalf("malformed token: %v", err)
		}
	}
	// Bound synthetic entries without allocating hundreds of host ports.
	for i := range maxPreparedLaunches {
		r.launches[string(rune(i))] = preparedAction{lease: &portLease{expires: time.Now().Add(time.Hour)}}
	}
	if _, _, err := r.reserve(plan); err == nil {
		t.Fatal("unbounded prepared actions")
	}
	for _, launch := range r.launches {
		launch.lease.expires = time.Time{}
	}
	r.pruneLaunches()
	if len(r.launches) != 0 {
		t.Fatal("expired launch tokens retained")
	}
}

func runtimeFixture(t *testing.T, overrides map[string]any) (*Runtime, secret.Controller) {
	t.Helper()
	c, workspace := actionControllerFixture(t, []string{"pwd"})
	policy := map[string]any{"command": []string{"pwd"}, "cwd": ".", "secrets": []string{"TOKEN"}, "all_secrets": false, "timeout_seconds": 10, "max_output_bytes": 4096,
		"dynamic_port": map[string]any{"preferred": 41280, "environment": "PORT", "origin_environment": "ORIGIN"}, "public_environment": []string{"PUBLIC_API"}, "preview_environment": map[string]string{"PUBLIC_API": "/api"}}
	for key, value := range overrides {
		policy[key] = value
	}
	encoded, _ := json.Marshal(policy)
	if _, err := c.ImportValues(t.Context(), "fixture", map[string]string{"PUBLIC_API": "http://127.0.0.1:41991/v1", "ORIGIN": "private-origin"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetAction(t.Context(), "fixture", "web", encoded); err != nil {
		t.Fatal(err)
	}
	r, err := NewRuntime(c, Layout{CallbackPort: new(int), Workspace: workspace, Binary: filepath.Join(t.TempDir(), "not-built"), Bwrap: "/usr/bin/bwrap", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), PreviewBaseDomain: "preview.example.test"}, process.ManagerOptions{MaxProcesses: 4, MaxOutputBytes: 4096, Retention: time.Minute, StopGrace: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	r.ports = testPorts()
	t.Cleanup(r.Close)
	return r, c
}

func TestPreparedMappingsAndFailedLaunchCleanup(t *testing.T) {
	r, _ := runtimeFixture(t, nil)
	request := PrepareRequest{Profile: "fixture", Action: "web"}
	if _, err := r.Prepare(t.Context(), PrepareRequest{Profile: "fixture", Action: "check"}); err == nil || err.Error() != "action does not use a dynamic port" {
		t.Fatalf("static prepare: %v", err)
	}
	prepared, err := r.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if prepared["expires_in_seconds"] != 30 || prepared["backend_routes"].(map[string]int)["/api"] != 41991 || prepared["environment_suffixes"].(map[string]string)["PUBLIC_API"] != "/v1" {
		t.Fatalf("prepare metadata: %#v", prepared)
	}
	token := prepared["launch_token"].(string)
	run := RunRequest{Profile: "fixture", Action: "web", LaunchToken: &token}
	if _, err = r.Run(t.Context(), run); err == nil || !strings.HasPrefix(err.Error(), "PREVIEW_MAPPING_REQUIRED") {
		t.Fatalf("missing public mapping: %v", err)
	}
	if len(r.launches) != 0 || len(r.ports.held) != 0 || len(r.List()["processes"].([]map[string]any)) != 0 {
		t.Fatal("invalid mapping launched or retained reservation")
	}
	if _, err = r.Run(t.Context(), run); err == nil || err.Error() != "action launch token is unknown or expired" {
		t.Fatalf("failed attempt token reused: %v", err)
	}
	prepared, err = r.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	token = prepared["launch_token"].(string)
	base := "https://loki-" + strings.Repeat("a", 32) + ".preview.example.test"
	run.PublicEnvironment = map[string]string{"ORIGIN": base, "PUBLIC_API": base + "/api/v1"}
	if _, err = r.Run(t.Context(), run); err == nil {
		t.Fatal("missing helper launched")
	}
	if len(r.ports.held) != 0 || len(r.launches) != 0 {
		t.Fatal("failed launch reservation retained")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = r.Prepare(ctx, request); err == nil || len(r.launches) != 0 {
		t.Fatal("canceled preparation reserved a port")
	}
	if _, err = r.Prepare(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	r.Close()
	if len(r.ports.held) != 0 || len(r.launches) != 0 {
		t.Fatal("runtime close retained prepared port")
	}
	if _, err = r.Prepare(t.Context(), request); err == nil {
		t.Fatal("closed runtime prepared action")
	}
}
