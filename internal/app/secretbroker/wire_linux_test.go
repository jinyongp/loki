//go:build linux

package secretbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/platform/privateio"
)

func testAddress(t *testing.T) Address {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "loki-wire-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return Address{RootID: strings.Repeat("a", 64), VaultID: strings.Repeat("b", 32), RunnerID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", InstanceID: "a203906e-1c9c-4cc5-acb7-a68d118c54da", Endpoint: filepath.Join(dir, "s"), Protocol: 1, RedactionProtocol: 1}
}

func serveTestReceiver(t *testing.T, r Receiver) (context.Context, context.CancelFunc) {
	t.Helper()
	listener, err := Listen(r.Address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = r.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("receiver did not stop")
		}
	})
	return ctx, cancel
}

func TestReceiverRejectsWrongBindingBeforePayload(t *testing.T) {
	a := testAddress(t)
	admitted := make(chan struct{}, 1)
	serveTestReceiver(t, Receiver{Address: a, Admit: func(context.Context, InputIdentity) error { admitted <- struct{}{}; return nil }, Accept: func(context.Context, *ReceivedInput) error { return nil }, Handle: func(context.Context, *ReceivedInput) error { return nil }})
	for _, change := range []string{"root", "vault", "runner", "instance", "server", "client", "execution", "job"} {
		conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: a.Endpoint, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var hello map[string]any
		if err = privateio.ReadControl(conn, &hello); err != nil {
			t.Fatal(err)
		}
		bind := bindFrame{Protocol: 1, Type: "bind", RootID: a.RootID, VaultID: a.VaultID, RunnerID: a.RunnerID, InstanceID: a.InstanceID, ServerChallenge: hello["server_challenge"].(string), ClientChallenge: strings.Repeat("c", 32), ExecutionID: strings.Repeat("d", 32), InputID: strings.Repeat("e", 32), ExternalExecutionID: strings.Repeat("f", 32)}
		switch change {
		case "root":
			bind.RootID = strings.Repeat("0", 64)
		case "vault":
			bind.VaultID = strings.Repeat("0", 32)
		case "runner":
			bind.RunnerID = "wrong"
		case "instance":
			bind.InstanceID = "wrong"
		case "server":
			bind.ServerChallenge = "wrong"
		case "client":
			bind.ClientChallenge = "wrong"
		case "execution":
			bind.ExecutionID = "wrong"
		case "job":
			bind.ExternalExecutionID = "wrong"
		}
		if err = privateio.WriteControl(conn, bind); err != nil {
			t.Fatal(err)
		}
		var ready map[string]any
		if privateio.ReadControl(conn, &ready) == nil {
			t.Fatal("invalid binding reached ready", change)
		}
		_ = conn.Close()
	}
	select {
	case <-admitted:
		t.Fatal("invalid binding reached admission")
	default:
	}
}

func TestListenerPreservesExistingAndReplacementEndpoint(t *testing.T) {
	a := testAddress(t)
	first, err := Listen(a)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if other, err := Listen(a); err == nil {
		other.Close()
		t.Fatal("existing endpoint replaced")
	}
	if err := os.Remove(a.Endpoint); err != nil {
		t.Fatal(err)
	}
	second, err := Listen(a)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = first.Close()
	conn, err := net.Dial("unix", a.Endpoint)
	if err != nil {
		t.Fatal("closing old listener removed successor", err)
	}
	conn.Close()
	_ = second.Close()
	if _, err := os.Lstat(a.Endpoint); !os.IsNotExist(err) {
		t.Fatal("owned endpoint not removed", err)
	}
}

func TestReceiverPayloadRequiresExactMaskingAndPrivateSerialization(t *testing.T) {
	b := bindFrame{ExecutionID: strings.Repeat("d", 32), InputID: strings.Repeat("e", 32), ExternalExecutionID: strings.Repeat("f", 32)}
	payload := map[string]any{"protocol": 1, "type": "payload", "execution_id": b.ExecutionID, "input_id": b.InputID, "external_execution_id": b.ExternalExecutionID, "delivery_id": strings.Repeat("a", 32), "values": []any{map[string]any{"key": "TOKEN", "kind": "secret", "value": "canary-token"}}, "redaction": map[string]any{"protocol": 1, "patterns": [][]byte{[]byte("canary-token")}}}
	body, _ := json.Marshal(payload)
	defer clear(body)
	p, err := decodePayload(body, b)
	if err != nil {
		t.Fatal(err)
	}
	defer p.destroy()
	if _, err = json.Marshal(p); err == nil {
		t.Fatal("private input serialized")
	}
	if strings.Contains(fmt.Sprintf("%+v %#v", p, p), "canary-token") {
		t.Fatal("private input formatted")
	}
	var output bytes.Buffer
	writer := p.Writer(&output)
	if _, err = writer.Write([]byte("canary-")); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write([]byte("token")); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil || output.String() != "[REDACTED]" {
		t.Fatal("split write leaked", err)
	}
	payload["redaction"] = map[string]any{"protocol": 1, "patterns": [][]byte{[]byte("different")}}
	body, _ = json.Marshal(payload)
	if bad, err := decodePayload(body, b); err == nil {
		bad.destroy()
		t.Fatal("wrong masking material accepted")
	}
}

// This test runs with the exact pinned CLI in a disposable non-root container.
// The container's PID namespace guarantees cleanup of its key-session daemon.
func TestReceiverRealProviderContract(t *testing.T) {
	testReceiverRealProvider(t, false)
}

// This deliberately violates the public-only dependency boundary to isolate
// the provider defect. It is never a substitute for the contract test above.
func TestReceiverRealProviderPrivateDirectoryControl(t *testing.T) {
	testReceiverRealProvider(t, true)
}

func testReceiverRealProvider(t *testing.T, privateDirectoryControl bool) {
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("pinned provider required")
		}
		t.Skip("LOKI_SECRET_TEST_DEVTOOLS not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	root := filepath.Join(t.TempDir(), "dvt")
	call := func(stdin string, args ...string) map[string]any {
		t.Helper()
		args = append(args, "--isolated-root", root)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdin = strings.NewReader(stdin)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		err := cmd.Run()
		var envelope struct {
			OK    bool           `json:"ok"`
			Data  map[string]any `json:"data"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		decodeErr := json.Unmarshal(output.Bytes(), &envelope)
		if err != nil || decodeErr != nil || !envelope.OK {
			t.Fatalf("provider %s: failed, code=%s exit=%v", args[0], envelope.Error.Code, err)
		}
		return envelope.Data
	}
	call("synthetic-private-passphrase", "vault", "init", "--backend", "passphrase", "--passphrase-stdin")
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, binary, "vault", "lock", "--isolated-root", root).Run()
	})
	revision := call("", "values", "revision", "--profile", "app-fixture")["revision"].(string)
	call("canary-token", "secret", "set", "TOKEN", "--stdin", "--profile", "app-fixture", "--revision", revision, "--request-id", "06cfc9b1-2bbf-42fb-81a1-0bd6ade00224")
	job := strings.Repeat("f", 32)
	prepBody, _ := json.Marshal(map[string]any{"external_execution_id": job, "selection": map[string]any{"keys": []string{"TOKEN"}}})
	prepared := call(string(prepBody), "values", "execution", "prepare", "--profile", "app-fixture", "--request-id", "f79c083d-ce81-43ce-b30e-710634ca5a96", "--stdin")["item"].(map[string]any)
	runner, instance := "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", "a203906e-1c9c-4cc5-acb7-a68d118c54da"
	addressData := call("", "values", "runner", "address", runner, "--instance", instance)
	raw, _ := json.Marshal(addressData)
	var a Address
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	if !a.valid() {
		t.Fatal("provider address incompatible")
	}
	if privateDirectoryControl {
		// Diagnostic control only: a production receiver must never reconstruct
		// provider-private layout. This run cannot satisfy S03 acceptance.
		t.Log("DIAGNOSTIC CONTROL: creating provider-private transport directory; not acceptance evidence")
		if err := os.MkdirAll(filepath.Join(root, "data", "value-runner-transports", runner, instance), 0700); err != nil {
			t.Fatal(err)
		}
	}
	finish := make(chan struct{})
	handled := make(chan string, 1)
	serveTestReceiver(t, Receiver{Address: a, Admit: func(_ context.Context, id InputIdentity) error {
		if id.ExecutionID != prepared["execution_id"] || id.InputID != prepared["input_id"] || id.ExternalExecutionID != job {
			return errWire
		}
		return nil
	}, Accept: func(_ context.Context, p *ReceivedInput) error {
		return p.WithValues(func(values, kinds map[string]string) error {
			if len(values) != 1 || values["TOKEN"] != "canary-token" || kinds["TOKEN"] != "secret" {
				return errWire
			}
			return nil
		})
	}, Handle: func(handler context.Context, p *ReceivedInput) error {
		var output bytes.Buffer
		writer := p.Writer(&output)
		defer writer.Abort()
		if _, err := writer.Write([]byte("before canary-")); err != nil {
			return err
		}
		select {
		case <-finish:
		case <-handler.Done():
			return handler.Err()
		}
		if _, err := writer.Write([]byte("token after")); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		handled <- output.String()
		return nil
	}})
	registration, _ := json.Marshal(map[string]any{"runner_id": runner, "instance_id": instance, "protocol": 1, "redaction_protocol": 1})
	call(string(registration), "values", "runner", "register", "--request-id", "bcd5460d-ae77-41ce-af38-2880495b9cc1", "--stdin")
	approval, _ := json.Marshal(map[string]any{"revision": prepared["revision"], "runner_id": runner, "runner_instance_id": instance, "external_execution_id": job, "input_id": prepared["input_id"]})
	approved := call(string(approval), "values", "execution", "approve", prepared["execution_id"].(string), "--profile", "app-fixture", "--request-id", "a942ee90-8635-4e91-8dd3-95bbe93c89a5", "--stdin")["item"].(map[string]any)
	delivery, _ := json.Marshal(map[string]any{"revision": approved["revision"]})
	delivered := call(string(delivery), "values", "execution", "deliver", prepared["execution_id"].(string), "--profile", "app-fixture", "--request-id", "a27d78d7-6f7c-4d81-ac37-4756715e464d", "--stdin")["item"].(map[string]any)
	if delivered["delivery_state"] != "delivered" {
		t.Fatal("provider did not accept receiver ACK")
	}
	// Query the same receipt while the encrypted control store is unlocked.
	// Lock preserves masking, but does not make encrypted receipts readable.
	replay := call(string(delivery), "values", "execution", "deliver", prepared["execution_id"].(string), "--profile", "app-fixture", "--request-id", "a27d78d7-6f7c-4d81-ac37-4756715e464d", "--stdin")
	if replay["replayed"] != true {
		t.Fatal("delivery receipt did not replay")
	}
	call("", "vault", "lock")
	close(finish)
	select {
	case output := <-handled:
		if output != "before [REDACTED] after" {
			t.Fatal("provider input was not masked across lock")
		}
	case <-ctx.Done():
		t.Fatal("handler did not finish")
	}
}
