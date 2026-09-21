//go:build integration

package packaging

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealComposeAcceptance(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "accept-loki-compose.sh")
	command := exec.CommandContext(t.Context(), "bash", script)
	command.Dir = root
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err = command.Run(); err != nil {
		t.Fatalf("real Compose acceptance failed: %v\n%s", err, strings.TrimSpace(output.String()))
	}
	t.Log(strings.TrimSpace(output.String()))
}
