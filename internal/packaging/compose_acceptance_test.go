package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeAcceptanceIsDisposableAndChecksLifecycle(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "accept-loki-compose.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"mktemp -d", "trap cleanup", "down --volumes --remove-orphans",
		"life install", "life restart", "life backup", "life restore",
		"life rotate-credentials", "life upgrade", "life rollback",
		"verify-loki-derived-image.sh", "--profile browser", "--profile signing",
		"LOKI_ACCEPTANCE_INVARIANT_PATHS", "cmp \"$before\" \"$after\"",
		"assert_networks", "assert_no_mount", "assert_not_inspectable",
		"buildx version", "buildx build --quiet --load",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("acceptance harness lacks %q", required)
		}
	}
	if strings.Contains(script, "playwright") {
		t.Fatal("Compose acceptance must use the packaged browser service")
	}
}
