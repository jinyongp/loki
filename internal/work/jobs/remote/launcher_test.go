package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/work/jobs"
)

type fakeResponse func(map[string]json.RawMessage) []byte

func fakeLauncherSocket(t *testing.T, respond fakeResponse) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "launcher.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.AcceptUnix()
		if err != nil {
			return
		}
		defer conn.Close()
		raw, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var request map[string]json.RawMessage
		if json.Unmarshal(raw, &request) != nil {
			return
		}
		response := respond(request)
		if len(response) != 0 {
			conn.Write(append(response, '\n'))
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("fake launcher did not stop")
		}
	})
	return socket
}

func validOptions(t *testing.T, socket string) Options {
	t.Helper()
	uid := uint32(os.Getuid())
	return Options{
		Socket:       socket,
		ExpectedUID:  &uid,
		PolicySHA256: strings.Repeat("a", 64),
		Timeout:      time.Second,
	}
}

func validWorkload() jobs.Workload {
	return jobs.Workload{
		ID:   strings.Repeat("b", 32),
		CWD:  ".",
		Argv: []string{"/bin/true", "argument"},
	}
}

func TestLauncherInjectsTrustedPolicyAndExactWireShape(t *testing.T) {
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		if len(request) != 5 {
			t.Fatalf("request keys = %#v", request)
		}
		var operation, id, policy, cwd string
		var argv []string
		for key, target := range map[string]any{
			"operation":     &operation,
			"id":            &id,
			"policy_sha256": &policy,
			"cwd":           &cwd,
			"argv":          &argv,
		} {
			raw, ok := request[key]
			if !ok || json.Unmarshal(raw, target) != nil {
				t.Fatalf("request[%q] = %s", key, raw)
			}
		}
		if operation != "run" || id != strings.Repeat("b", 32) ||
			policy != strings.Repeat("a", 64) || cwd != "." ||
			len(argv) != 2 || argv[0] != "/bin/true" || argv[1] != "argument" {
			t.Fatalf("request = %#v", request)
		}
		return []byte(`{"ok":true,"result":{"exit_code":9}}`)
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	workload := validWorkload()
	result, err := launcher.Run(t.Context(), workload)
	if err != nil {
		t.Fatal(err)
	}
	workload.Argv[1] = "mutated"
	if result.ExitCode != 9 {
		t.Fatalf("result = %#v", result)
	}
}

func TestLauncherStrictlyRejectsInvalidResult(t *testing.T) {
	for _, response := range [][]byte{
		[]byte(`{"ok":true,"result":{"exit_code":0,"extra":true}}`),
		[]byte(`{"ok":true,"result":{"exit_code":999}}`),
	} {
		t.Run(string(response), func(t *testing.T) {
			socket := fakeLauncherSocket(t, func(map[string]json.RawMessage) []byte { return response })
			launcher, err := New(validOptions(t, socket))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = launcher.Run(t.Context(), validWorkload()); err == nil {
				t.Fatal("invalid launcher result was accepted")
			}
		})
	}
}

func TestLauncherRejectsUntrustedPeer(t *testing.T) {
	socket := fakeLauncherSocket(t, func(map[string]json.RawMessage) []byte {
		return []byte(`{"ok":true,"result":{"exit_code":0}}`)
	})
	options := validOptions(t, socket)
	uid := uint32(os.Getuid()) ^ 1
	options.ExpectedUID = &uid
	launcher, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launcher.Run(t.Context(), validWorkload()); err == nil ||
		!strings.Contains(err.Error(), "socket owner is not trusted") {
		t.Fatalf("peer error = %v", err)
	}
}

func TestLauncherConfigurationFailsClosed(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "launcher.sock")
	uid := uint32(os.Getuid())
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{"relative-socket", func(o *Options) { o.Socket = "launcher.sock" }},
		{"root-socket", func(o *Options) { o.Socket = "/" }},
		{"missing-uid", func(o *Options) { o.ExpectedUID = nil }},
		{"bad-policy", func(o *Options) { o.PolicySHA256 = "bad" }},
		{"short-timeout", func(o *Options) { o.Timeout = 0 }},
		{"long-timeout", func(o *Options) { o.Timeout = maxRunTimeout + time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := Options{Socket: socket, ExpectedUID: &uid, PolicySHA256: strings.Repeat("a", 64), Timeout: time.Second}
			test.mutate(&options)
			if _, err := New(options); err == nil {
				t.Fatal("invalid remote launcher configuration was accepted")
			}
		})
	}
}

func TestLauncherPropagatesRPCFailure(t *testing.T) {
	socket := fakeLauncherSocket(t, func(map[string]json.RawMessage) []byte {
		return []byte(`{"ok":false,"error":"synthetic launcher rejection"}`)
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	_, err = launcher.Run(context.Background(), validWorkload())
	if err == nil || !strings.Contains(err.Error(), "synthetic launcher rejection") {
		t.Fatalf("RPC failure = %v", err)
	}
}

func TestNilLauncherFailsClosed(t *testing.T) {
	var launcher *Launcher
	if _, err := launcher.Run(t.Context(), validWorkload()); err == nil {
		t.Fatal("nil launcher was accepted")
	}
}
