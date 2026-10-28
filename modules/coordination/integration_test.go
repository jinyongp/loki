package devtools

import (
	"encoding/json"
	"os"
	"testing"
)

// TestRealCoordinationCandidate verifies the installed contract in an isolated
// workspace, then proves process execution is rejected before any command runs.
func TestRealCoordinationCandidate(t *testing.T) {
	binary := os.Getenv("LOKI_DEVTOOLS_BINARY")
	if binary == "" {
		t.Skip("LOKI_DEVTOOLS_BINARY is not configured")
	}
	dir := t.TempDir()
	client, err := NewClient(binary, dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	candidate, err := client.Verify(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ProtocolVersion != 19 || candidate.ApprovedCommands != 19 {
		t.Fatalf("candidate: %#v", candidate)
	}
	for _, name := range []string{"process start", "process restart", "run"} {
		if _, err := client.call(t.Context(), name, json.RawMessage(`{}`), nil); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}
