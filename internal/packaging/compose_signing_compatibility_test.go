package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestComposeSigningConfigurationIsAdditiveForOlderLaunchers(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "compose.yaml"),
		filepath.Join("..", "host", "assets", "files", "compose.yaml"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Services map[string]struct {
				Command     []string          `yaml:"command"`
				Environment map[string]string `yaml:"environment"`
			} `yaml:"services"`
		}
		if err = yaml.Unmarshal(raw, &document); err != nil {
			t.Fatal(err)
		}
		launcher := document.Services["launcher"]
		for _, arg := range launcher.Command {
			if strings.HasPrefix(arg, "--signing-") {
				t.Fatalf("%s requires a new launcher flag during older-release recovery: %s", path, arg)
			}
		}
		for _, name := range []string{"LOKI_SIGNING_SOCKET_VOLUME", "LOKI_SIGNING_PUBLIC_KEY_FILE", "LOKI_SIGNING_GIT_CONFIG_FILE", "LOKI_SIGNING_ALLOWED_SIGNERS_FILE"} {
			if launcher.Environment[name] != "${"+name+":-}" {
				t.Fatalf("%s missing additive signing setting %s", path, name)
			}
		}
	}
}
