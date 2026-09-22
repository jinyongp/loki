package packaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeAcceptanceIsDisposableTopologySmoke(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "verify", "accept-compose.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	for _, required := range []string{
		"mktemp -d", "trap cleanup", "down --volumes --remove-orphans",
		"setfacl -m u:10000:rwx,d:u:10000:rwx",
		"compose config --quiet", "compose up -d --remove-orphans", "compose restart",
		"verify-derived-image.sh", "--profile browser", "--profile signing",
		"LOKI_ACCEPTANCE_INVARIANT_PATHS", "cmp \"$before\" \"$after\"",
		"assert_networks", "assert_no_mount", "assert_not_inspectable",
		"buildx version", "buildx build --quiet --load",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("acceptance harness lacks %q", required)
		}
	}
	for _, forbidden := range []string{
		"loki-compose-lifecycle.sh", "life backup", "life restore", "life upgrade", "life rollback",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("Compose topology acceptance still owns lifecycle transaction behavior %q", forbidden)
		}
	}
	if strings.Contains(script, "playwright") {
		t.Fatal("Compose acceptance must use the packaged browser service")
	}
}
