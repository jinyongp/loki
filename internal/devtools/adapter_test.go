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
	catalog := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalog, embeddedCatalog, 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "devtools")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.10.0\",\"commit\":\"test\",\"protocol_version\":3}}'; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"schema --all\" ]; then cat \"" + catalog + "\"; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > \"" + log + "\"\n" +
		"printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"item\":{},\"changed\":true,\"replayed\":false}}'\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(script, dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client, log
}

func TestClientBuildsArgumentsWithoutShell(t *testing.T) {
	client, log := fakeClient(t)
	marker := filepath.Join(client.CWD, "executed")
	value := "$(touch executed)"
	if err := os.Mkdir(filepath.Join(client.CWD, value), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"args": []string{"web"}, "dir": value, "request-id": "00000000-0000-0000-0000-000000000000"})
	result, err := client.Call(context.Background(), "process start", raw)
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
	for _, want := range []string{"process", "start", "--dir", value, "--request-id", "00000000-0000-0000-0000-000000000000", "web"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("arguments %q do not contain %q", args, want)
		}
	}
}

func TestClientConfinesProcessDirectory(t *testing.T) {
	client, _ := fakeClient(t)
	request := func(directory string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"args": []string{"web"}, "dir": directory, "request-id": "00000000-0000-0000-0000-000000000000"})
		return raw
	}
	if _, err := client.Call(context.Background(), "process start", request("/tmp")); err == nil {
		t.Fatal("outside process directory accepted")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(client.CWD, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "process start", request("linked")); err == nil {
		t.Fatal("symlinked process directory accepted")
	}
}

func TestClientRejectsInvalidAndUnsafeCalls(t *testing.T) {
	client, _ := fakeClient(t)
	if _, err := client.Call(context.Background(), "run", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unapproved command accepted")
	}
	if _, err := client.Call(context.Background(), "process start", json.RawMessage(`{"args":[],"request-id":"00000000-0000-0000-0000-000000000000"}`)); err == nil {
		t.Fatal("invalid positional arguments accepted")
	}
	if _, err := client.Call(context.Background(), "process start", json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000","capture-logs":true}`)); err == nil {
		t.Fatal("raw process logs accepted")
	}
}

func TestClientRejectsInvalidVersionAndTimeout(t *testing.T) {
	client, _ := fakeClient(t)
	client.Binary = filepath.Join(client.CWD, "wrong")
	if err := os.WriteFile(client.Binary, []byte("#!/bin/sh\nprintf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"latest\",\"commit\":\"test\",\"protocol_version\":3}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "process start", json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000"}`)); err == nil {
		t.Fatal("invalid version accepted")
	}

	client, _ = fakeClient(t)
	client.Binary = filepath.Join(client.CWD, "slow")
	if err := os.WriteFile(client.Binary, []byte("#!/bin/sh\nsleep 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client.Timeout = 20 * time.Millisecond
	if _, err := client.Call(context.Background(), "process start", json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000"}`)); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestClientRejectsIncompatibleCatalog(t *testing.T) {
	client, _ := fakeClient(t)
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.10.0\",\"commit\":\"test\",\"protocol_version\":3}}'; exit 0; fi\n" +
		"printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"protocol_version\":2,\"commands\":[]}}'\n"
	if err := os.WriteFile(client.Binary, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "process start", json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000"}`)); err == nil || !strings.Contains(err.Error(), "protocol version") {
		t.Fatalf("catalog error = %v", err)
	}
}

func TestClientRejectsPrivilegedDelegatedIdentity(t *testing.T) {
	client, _ := fakeClient(t)
	client.Identity = &process.Identity{}
	if _, err := client.Call(context.Background(), "process start", json.RawMessage(`{"args":["web"],"request-id":"00000000-0000-0000-0000-000000000000"}`)); err == nil || !strings.Contains(err.Error(), "unprivileged") {
		t.Fatalf("identity error = %v", err)
	}
}

func TestClientVerifyCachesAcceptedCandidate(t *testing.T) {
	dir := t.TempDir()
	catalog := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalog, embeddedCatalog, 0600); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls.log")
	script := filepath.Join(dir, "devtools")
	body := "#!/bin/sh\n" +
		"printf '%s %s\\n' \"$1\" \"$2\" >> \"" + calls + "\"\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.17.0\",\"commit\":\"candidate-test\",\"protocol_version\":3}}'; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"schema --all\" ]; then /bin/cat \"" + catalog + "\"; exit 0; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(script, dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	first, err := client.Verify(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Verify(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("candidate changed across cached verify: %#v %#v", first, second)
	}
	if first.Version != "0.17.0" || first.Commit != "candidate-test" || first.ProtocolVersion != ProtocolVersion ||
		first.ApprovedCommands != len(approvedNames) || len(first.CatalogSHA256) != 64 {
		t.Fatalf("candidate = %#v", first)
	}
	raw, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(raw))
	if len(lines) != 3 || strings.Count(string(raw), "version") != 1 || strings.Count(string(raw), "schema --all") != 1 {
		t.Fatalf("candidate verification was rerun: %q", raw)
	}
}

func TestCandidateFingerprintTracksApprovedContractOnly(t *testing.T) {
	commands, err := EmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	version := Version{Version: "0.17.0", Commit: "test", ProtocolVersion: ProtocolVersion}
	first, err := candidateEvidence(version, commands)
	if err != nil {
		t.Fatal(err)
	}
	copyCommands := append([]Command(nil), commands...)
	copyCommands[0].Description += " changed"
	second, err := candidateEvidence(version, copyCommands)
	if err != nil {
		t.Fatal(err)
	}
	if first.CatalogSHA256 == second.CatalogSHA256 {
		t.Fatal("approved contract change did not change candidate fingerprint")
	}
}
