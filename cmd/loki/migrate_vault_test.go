package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/secret"
	"loki/internal/state"
)

func pythonVaultCopy(t *testing.T, root string) ([]byte, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "internal", "state", "testdata", "python-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Key      string          `json:"key_hex"`
		Envelope json.RawMessage `json:"envelope"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(fixture.Key)
	if err != nil {
		t.Fatal(err)
	}
	envelope := append([]byte(nil), fixture.Envelope...)
	envelope = append(envelope, '\n')
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "store.json"), envelope, 0600); err != nil {
		t.Fatal(err)
	}
	return key, envelope
}

func TestMigrateVaultCLIImportsAndRestoresWithoutSecretOutput(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "python-copy")
	destination := filepath.Join(parent, "go")
	restore := filepath.Join(parent, "python-restore")
	key, envelope := pythonVaultCopy(t, source)
	var stdout, stderr bytes.Buffer
	arguments := []string{"migrate-vault", "import", "--source-copy", source, "--destination", destination}
	if code := run(arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("import code = %d, stderr = %s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "synthetic-fixture") || strings.Contains(stdout.String(), "TEST_VALUE") {
		t.Fatal("migration output exposed secret material")
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["migrated"] != true || result["profiles"] != float64(1) || result["secrets"] != float64(1) {
		t.Fatalf("migration result = %#v", result)
	}
	snapshot, err := (state.Store{Dir: destination, Validate: secret.Validate}).Load(t.Context())
	if err != nil || snapshot.Revision != 1 {
		t.Fatal("migrated vault readback:", err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(arguments, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "\"reused\": true") {
		t.Fatalf("idempotent import code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"migrate-vault", "restore", "--migration", destination, "--destination", restore}, &stdout, &stderr); code != 0 {
		t.Fatalf("restore code = %d, stderr = %s", code, stderr.String())
	}
	gotKey, err := os.ReadFile(filepath.Join(restore, "master.key"))
	if err != nil || !bytes.Equal(gotKey, key) {
		t.Fatal("restored key differs:", err)
	}
	gotEnvelope, err := os.ReadFile(filepath.Join(restore, "store.json"))
	if err != nil || !bytes.Equal(gotEnvelope, envelope) {
		t.Fatal("restored envelope differs:", err)
	}
}

func TestMigrateVaultCLIImportsIntoExistingRuntimeDirectory(t *testing.T) {
	parent := t.TempDir()
	source := filepath.Join(parent, "python-copy")
	destination := filepath.Join(parent, "runtime-state")
	pythonVaultCopy(t, source)
	if err := os.Mkdir(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "audit.jsonl"), []byte("preserve\n"), 0600); err != nil {
		t.Fatal(err)
	}
	arguments := []string{
		"migrate-vault", "import",
		"--source-copy", source,
		"--destination", destination,
		"--existing-destination",
	}
	var stdout, stderr bytes.Buffer
	if code := run(arguments, &stdout, &stderr); code != 0 {
		t.Fatalf("existing import code = %d, stderr = %s", code, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["migrated"] != true || result["reused"] != false || result["existing_destination"] != true {
		t.Fatalf("existing migration result = %#v", result)
	}
	if got, err := os.ReadFile(filepath.Join(destination, "audit.jsonl")); err != nil || string(got) != "preserve\n" {
		t.Fatalf("unrelated runtime state changed: %q %v", got, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(arguments, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "\"reused\": true") {
		t.Fatalf("existing idempotent import code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
}

func TestMigrateVaultCLIRequiresOfflineCopyAndCleanPaths(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"migrate-vault", "import", "--source-copy", "/var/lib/loki/runtime", "--destination", "/tmp/go-vault"}, &stdout, &stderr); code != 1 {
		t.Fatalf("live source code = %d", code)
	}
	if code := run([]string{"migrate-vault", "import", "--source-copy", "relative", "--destination", "/tmp/go-vault"}, &stdout, &stderr); code != 2 {
		t.Fatalf("relative source code = %d", code)
	}
}
