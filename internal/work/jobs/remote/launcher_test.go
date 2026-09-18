package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/work/jobs"
)

type fakeResponse func(map[string]json.RawMessage) []byte

type requestLog struct {
	mu         sync.Mutex
	operations []string
}

func (l *requestLog) add(request map[string]json.RawMessage) {
	var operation string
	_ = json.Unmarshal(request["operation"], &operation)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.operations = append(l.operations, operation)
}

func (l *requestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.operations...)
}

func fakeLauncherSocket(t *testing.T, respond fakeResponse) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "launcher.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
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
					_, _ = conn.Write(append(response, '\n'))
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("fake launcher accept loop did not stop")
		}
		waitDone := make(chan struct{})
		go func() {
			workers.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(time.Second):
			t.Fatal("fake launcher workers did not stop")
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

func TestLauncherUsesStartThenWaitWithTrustedPolicy(t *testing.T) {
	log := &requestLog{}
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		log.add(request)
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		switch operation {
		case "start":
			if len(request) != 5 {
				t.Fatalf("start request keys = %#v", request)
			}
			var id, policy, cwd string
			var argv []string
			for key, target := range map[string]any{
				"id": &id, "policy_sha256": &policy, "cwd": &cwd, "argv": &argv,
			} {
				raw, ok := request[key]
				if !ok || json.Unmarshal(raw, target) != nil {
					t.Fatalf("start request[%q] = %s", key, raw)
				}
			}
			if id != strings.Repeat("b", 32) || policy != strings.Repeat("a", 64) ||
				cwd != "." || len(argv) != 2 || argv[0] != "/bin/true" || argv[1] != "argument" {
				t.Fatalf("start request = %#v", request)
			}
			return []byte("{\"ok\":true,\"result\":{\"id\":\"" + id + "\"}}")
		case "wait":
			if len(request) != 2 {
				t.Fatalf("wait request keys = %#v", request)
			}
			return []byte("{\"ok\":true,\"result\":{\"exit_code\":9}}")
		default:
			t.Fatalf("unexpected operation %q", operation)
			return nil
		}
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
	if got := strings.Join(log.snapshot(), ","); got != "start,wait" {
		t.Fatalf("operations = %q", got)
	}
}

func TestLauncherCancelsAfterWaitFailure(t *testing.T) {
	log := &requestLog{}
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		log.add(request)
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		switch operation {
		case "start":
			return []byte("{\"ok\":true,\"result\":{\"id\":\"" + strings.Repeat("b", 32) + "\"}}")
		case "wait":
			return []byte("{\"ok\":false,\"error\":\"synthetic wait failure\"}")
		case "cancel":
			return []byte("{\"ok\":true,\"result\":{\"canceled\":true}}")
		default:
			return []byte("{\"ok\":false,\"error\":\"unexpected\"}")
		}
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launcher.Run(t.Context(), validWorkload()); err == nil || !strings.Contains(err.Error(), "synthetic wait failure") {
		t.Fatalf("wait failure = %v", err)
	}
	if got := strings.Join(log.snapshot(), ","); got != "start,wait,cancel" {
		t.Fatalf("operations = %q", got)
	}
}

func TestLauncherCancelsAfterUncertainStart(t *testing.T) {
	log := &requestLog{}
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		log.add(request)
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		if operation == "start" {
			return nil
		}
		if operation == "cancel" {
			return []byte("{\"ok\":true,\"result\":{\"canceled\":false}}")
		}
		return []byte("{\"ok\":false,\"error\":\"unexpected\"}")
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launcher.Run(t.Context(), validWorkload()); err == nil {
		t.Fatal("uncertain start returned success")
	}
	if got := strings.Join(log.snapshot(), ","); got != "start,cancel" {
		t.Fatalf("operations = %q", got)
	}
}

func TestLauncherStrictlyRejectsInvalidResultsAndCleansUp(t *testing.T) {
	for _, response := range []string{
		"{\"ok\":true,\"result\":{\"exit_code\":0,\"extra\":true}}",
		"{\"ok\":true,\"result\":{\"exit_code\":999}}",
	} {
		t.Run(response, func(t *testing.T) {
			log := &requestLog{}
			socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
				log.add(request)
				var operation string
				_ = json.Unmarshal(request["operation"], &operation)
				switch operation {
				case "start":
					return []byte("{\"ok\":true,\"result\":{\"id\":\"" + strings.Repeat("b", 32) + "\"}}")
				case "wait":
					return []byte(response)
				case "cancel":
					return []byte("{\"ok\":true,\"result\":{\"canceled\":false}}")
				default:
					return nil
				}
			})
			launcher, err := New(validOptions(t, socket))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = launcher.Run(t.Context(), validWorkload()); err == nil {
				t.Fatal("invalid launcher result was accepted")
			}
			if got := strings.Join(log.snapshot(), ","); got != "start,wait,cancel" {
				t.Fatalf("operations = %q", got)
			}
		})
	}
}

func TestLauncherRejectsUntrustedPeer(t *testing.T) {
	socket := fakeLauncherSocket(t, func(map[string]json.RawMessage) []byte {
		return []byte("{\"ok\":true,\"result\":{\"id\":\"" + strings.Repeat("b", 32) + "\"}}")
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

func TestNilLauncherFailsClosed(t *testing.T) {
	var launcher *Launcher
	if _, err := launcher.Run(context.Background(), validWorkload()); err == nil {
		t.Fatal("nil launcher was accepted")
	}
}
