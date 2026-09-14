package devtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/process"
)

func fakeClient(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	script := filepath.Join(dir, "devtools")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.8.2\",\"commit\":\"test\"}}'; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > \"" + log + "\"\n" +
		"if [ \"$1 $2\" = \"variable set\" ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"profile\":\"test\",\"changed\":true}}'; else printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"profile\":\"test\",\"envs\":[]}}'; fi\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(script, dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	return client, log
}

func TestClientBuildsArgumentsWithoutShell(t *testing.T) {
	client, log := fakeClient(t)
	marker := filepath.Join(client.CWD, "executed")
	value := "$(touch " + marker + ")"
	raw, _ := json.Marshal(map[string]any{"profile": "test", "args": []string{"KEY"}, "value": value})
	result, err := client.Call(context.Background(), "variable set", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(result) {
		t.Fatal("invalid result data")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("argument was interpreted by a shell")
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"variable", "set", "--profile", "test", "--value", value, "KEY"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("arguments %q do not contain %q", args, want)
		}
	}
}

func TestClientRejectsInvalidAndUnsafeCalls(t *testing.T) {
	client, _ := fakeClient(t)
	if _, err := client.Call(context.Background(), "run", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unapproved command accepted")
	}
	if _, err := client.Call(context.Background(), "variable set", json.RawMessage(`{"args":[],"value":"x"}`)); err == nil {
		t.Fatal("invalid positional arguments accepted")
	}
	if _, err := client.Call(context.Background(), "process start", json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000","capture-logs":true}`)); err == nil {
		t.Fatal("raw process logs accepted")
	}
}

func TestClientRejectsVersionDriftAndTimeout(t *testing.T) {
	client, _ := fakeClient(t)
	client.Binary = filepath.Join(client.CWD, "wrong")
	if err := os.WriteFile(client.Binary, []byte("#!/bin/sh\nprintf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.9.0\",\"commit\":\"test\"}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "env list", json.RawMessage(`{"profile":"test"}`)); err == nil {
		t.Fatal("version drift accepted")
	}

	client, _ = fakeClient(t)
	client.Binary = filepath.Join(client.CWD, "slow")
	if err := os.WriteFile(client.Binary, []byte("#!/bin/sh\nsleep 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client.Timeout = 20 * time.Millisecond
	if _, err := client.Call(context.Background(), "env list", json.RawMessage(`{"profile":"test"}`)); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestClientRejectsPrivilegedDelegatedIdentity(t *testing.T) {
	client, _ := fakeClient(t)
	client.Identity = &process.Identity{}
	if _, err := client.Call(context.Background(), "env list", json.RawMessage(`{"profile":"test"}`)); err == nil || !strings.Contains(err.Error(), "unprivileged") {
		t.Fatalf("identity error = %v", err)
	}
}
