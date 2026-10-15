package browser

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordingAdapterPreservesActions(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("recording adapter checks require Node.js")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "testdata/recording.cjs", filepath.Join("adapters", "playwright.cjs"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recording adapter: %v\n%s", err, output)
	}
}
