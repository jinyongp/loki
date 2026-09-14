package devtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		"exec = [\"sh\", \"-c\", \"printf '%s' \\\"$TOKEN\\\" > inherited.txt\"]\n"
	if err := os.WriteFile(filepath.Join(root, "devtools.toml"), []byte(configuration), 0600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(binary, root, []string{
		"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"PATH=" + filepath.Dir(binary) + ":/usr/bin:/bin", "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
	})
	if err != nil {
		t.Fatal(err)
	}
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
	request := json.RawMessage(`{"args":["probe"],"dir":"` + root + `","request-id":"6ab1d7f0-21b6-4d0b-9f47-83be95872c51"}`)
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
	if string(inherited) != private {
		t.Fatalf("managed process inherited %q", inherited)
	}
}
