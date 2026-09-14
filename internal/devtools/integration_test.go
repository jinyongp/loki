package devtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/execution"
	"loki/internal/secret"
)

// TestRealProcessInheritsBrokerSecrets runs only when the pinned release binary
// is supplied explicitly. It uses isolated project, HOME, and vault directories.
func TestRealProcessInheritsBrokerSecrets(t *testing.T) {
	binary := os.Getenv("LOKI_DEVTOOLS_BINARY")
	if binary == "" {
		t.Skip("LOKI_DEVTOOLS_BINARY is not configured")
	}
	root := t.TempDir()
	resultPath := filepath.Join(root, "inherited.txt")
	configuration := "profile = \"loki-broker-test\"\n\n" +
		"[commands.probe]\n" +
		"exec = [\"sh\", \"-c\", \"printf '%s\\\\n%s' \\\"$TOKEN\\\" \\\"${HTTPS_PROXY-unset}\\\" > inherited.txt\"]\n"
	if err := os.WriteFile(filepath.Join(root, "devtools.toml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	contractRaw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "go", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(contractRaw)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "runner")
	cache := filepath.Join(root, "cache")
	temp := filepath.Join(root, "temp")
	for name, path := range map[string]string{"runner-state": state, "runner-cache": cache, "runner-temp": temp} {
		directory := contract.Directories[name]
		directory.Path = path
		contract.Directories[name] = directory
	}
	contract.Environment["XDG_CONFIG_HOME"] = filepath.Join(state, "config")
	contract.Environment["GH_CONFIG_DIR"] = filepath.Join(state, "config", "gh")
	contract.Environment["XDG_DATA_HOME"] = filepath.Join(state, "data")
	contract.Environment["XDG_STATE_HOME"] = filepath.Join(state, "state")
	contract.Environment["XDG_CACHE_HOME"] = cache
	contract.Environment["NPM_CONFIG_CACHE"] = filepath.Join(cache, "npm")
	contract.Environment["npm_config_store_dir"] = filepath.Join(cache, "pnpm")
	contract.Environment["PLAYWRIGHT_BROWSERS_PATH"] = filepath.Join(cache, "playwright")
	contract.Environment["GOCACHE"] = filepath.Join(cache, "go-build")
	contract.Environment["GOMODCACHE"] = filepath.Join(cache, "go-mod")
	contract.Environment["PIP_CACHE_DIR"] = filepath.Join(cache, "pip")
	contract.Environment["TMPDIR"] = temp
	if err = os.MkdirAll(temp, 0700); err != nil {
		t.Fatal(err)
	}
	environment, err := contract.EnvironmentList()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(binary, root, environment)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	vault := filepath.Join(root, "vault")
	if err = os.Mkdir(vault, 0700); err != nil {
		t.Fatal(err)
	}
	controller := secret.Controller{StateDirectory: vault}
	ctx := context.Background()
	if _, err = controller.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = controller.CreateProfile(ctx, "project"); err != nil {
		t.Fatal(err)
	}
	const private = "synthetic-broker-secret"
	if _, err = controller.SetSecret(ctx, "project", "TOKEN", private, false); err != nil {
		t.Fatal(err)
	}
	request := json.RawMessage(`{"args":["probe"],"dir":".","request-id":"6ab1d7f0-21b6-4d0b-9f47-83be95872c51"}`)
	result, err := (Broker{Client: client, Secrets: controller}).Call(ctx, "process start", request, "project", []string{"TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result), private) {
		t.Fatal("broker returned the secret")
	}
	var inherited []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		inherited, err = os.ReadFile(resultPath)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	if string(inherited) != private+"\nunset" {
		t.Fatalf("managed process inherited %q", inherited)
	}
}
