package devtools

import (
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
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.10.0\",\"commit\":\"test\",\"protocol_version\":10}}'; exit 0; fi\n" +
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

func TestClientRejectsGenericExecutionBeforeLaunching(t *testing.T) {
	client, log := fakeClient(t)
	client.Binary = "/does/not/exist"
	for _, name := range []string{"process start", "process restart", "run", "command run", "project inspect", "task claim"} {
		if _, err := client.Call(t.Context(), name, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("generic command accepted: %s", name)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("generic command launched devtools")
	}
	for _, name := range []string{"process start", "process restart", "run"} {
		if _, err := client.call(t.Context(), name, json.RawMessage(`{}`), nil); err == nil || !strings.Contains(err.Error(), "not approved") {
			t.Fatalf("internal escape hatch %s: %v", name, err)
		}
	}
}

func TestClientRejectsInvalidVersionAndTimeout(t *testing.T) {
	client, _ := fakeClient(t)
	if err := os.WriteFile(client.Binary, []byte("#!/bin/sh\nprintf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"latest\",\"commit\":\"test\",\"protocol_version\":10}}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Verify(t.Context()); err == nil {
		t.Fatal("invalid version accepted")
	}
	client, _ = fakeClient(t)
	if err := os.WriteFile(client.Binary, []byte("#!/bin/sh\nsleep 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client.Timeout = 20 * time.Millisecond
	if _, err := client.Verify(t.Context()); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestClientRejectsPrivilegedDelegatedIdentity(t *testing.T) {
	client, _ := fakeClient(t)
	client.Identity = &process.Identity{}
	if _, err := client.Verify(t.Context()); err == nil || !strings.Contains(err.Error(), "unprivileged") {
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
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.17.0\",\"commit\":\"candidate-test\",\"protocol_version\":10}}'; exit 0; fi\n" +
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

func TestCandidateFingerprintTracksExecutableContractOnly(t *testing.T) {
	commands, err := EmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	version := Version{Version: "0.17.0", Commit: "test", ProtocolVersion: ProtocolVersion}
	first, err := candidateEvidence(version, commands)
	if err != nil {
		t.Fatal(err)
	}
	descriptionOnly := append([]Command(nil), commands...)
	descriptionOnly[0].Description += " editorial change"
	second, err := candidateEvidence(version, descriptionOnly)
	if err != nil {
		t.Fatal(err)
	}
	if first.CatalogSHA256 != second.CatalogSHA256 {
		t.Fatal("non-executable description text changed candidate fingerprint")
	}

	contractChange := append([]Command(nil), commands...)
	contractChange[0].InputSchema = json.RawMessage(`{"type":"object","additionalProperties":true}`)
	third, err := candidateEvidence(version, contractChange)
	if err != nil {
		t.Fatal(err)
	}
	if first.CatalogSHA256 == third.CatalogSHA256 {
		t.Fatal("approved executable contract change did not change candidate fingerprint")
	}
}

func TestClientVerifyRejectsApprovedContractDrift(t *testing.T) {
	var envelope map[string]any
	if err := json.Unmarshal(embeddedCatalog, &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope["data"].(map[string]any)
	commands := data["commands"].([]any)
	found := false
	for _, raw := range commands {
		command := raw.(map[string]any)
		if command["name"] != "task checkpoint" {
			continue
		}
		found = true
		options := command["options"].([]any)
		command["options"] = append(options, map[string]any{"name": "compaction-fingerprint"})
		input := command["input_schema"].(map[string]any)
		properties := input["properties"].(map[string]any)
		properties["compaction-fingerprint"] = map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	}
	if !found {
		t.Fatal("task checkpoint fixture missing")
	}
	catalog, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalogPath, catalog, 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "devtools")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.17.0\",\"commit\":\"drift-test\",\"protocol_version\":10}}'; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"schema --all\" ]; then /bin/cat \"" + catalogPath + "\"; exit 0; fi\n" +
		"exit 2\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(script, dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if _, err := client.Verify(t.Context()); err == nil || !strings.Contains(err.Error(), "does not match the Loki baseline") {
		t.Fatalf("approved contract drift was accepted: %v", err)
	}
}
