package devtools

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealCoordinationCandidate verifies the installed contract in an isolated
// workspace, then proves process execution is rejected before any command runs.
func TestRealCoordinationCandidate(t *testing.T) {
	testRealCoordinationCandidate(t, os.Getenv("LOKI_DEVTOOLS_BINARY"))
}

func TestRealCoordinationPreviousState(t *testing.T) {
	previous := os.Getenv("LOKI_DEVTOOLS_PREVIOUS_BINARY")
	if previous == "" {
		t.Skip("previous provider is supplied by the native values Actions suite")
	}
	testRealCoordinationCandidate(t, previous)
}

func testRealCoordinationCandidate(t *testing.T, seedBinary string) {
	t.Helper()
	binary := os.Getenv("LOKI_DEVTOOLS_BINARY")
	if binary == "" {
		t.Skip("LOKI_DEVTOOLS_BINARY is not configured")
	}
	dir := t.TempDir()
	client, err := NewClient(binary, dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "DEVTOOLS_FORMAT=text"})
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
	if err := os.WriteFile(filepath.Join(dir, "devtools.toml"), []byte("profile = 'fixture'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Fixture creation is outside the adapter's runtime authority and uses only
	// this test's private HOME. Normal reads and mutations use the typed client.
	seedArgs := []string{"task", "add", "--title", "synthetic protocol upgrade", "--request-id", "123e4567-e89b-42d3-a456-426614174401"}
	if seedBinary == binary {
		seedArgs = append([]string{"--format", "json"}, seedArgs...)
	}
	seed := exec.CommandContext(t.Context(), seedBinary, seedArgs...)
	seed.Dir, seed.Env = dir, client.Env
	raw, err := seed.Output()
	if err != nil {
		t.Fatal("create synthetic task:", err)
	}
	var created struct {
		Data struct {
			Item struct {
				ID string `json:"id"`
			} `json:"item"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &created); err != nil || created.Data.Item.ID == "" {
		t.Fatal("synthetic task ID missing")
	}
	claimRequest := CoordinationMutationRequest{
		Target: created.Data.Item.ID, RequestID: "123e4567-e89b-42d3-a456-426614174402",
	}
	claim, err := client.MutateCoordination(t.Context(), ".", CoordinationClaim, claimRequest)
	if err != nil || !claim.ContextValid || claim.Context == "" || claim.RunID == "" {
		t.Fatal("claim did not return a valid private run:", err)
	}
	replay, err := client.MutateCoordination(t.Context(), ".", CoordinationClaim, claimRequest)
	if err != nil || replay.RunID != claim.RunID {
		t.Fatal("claim retry did not preserve the run:", err)
	}
	if strings.Contains(string(claim.Public), claim.Context) {
		t.Fatal("private claim context leaked into public output")
	}
	for index, action := range []CoordinationMutation{CoordinationCheckpoint, CoordinationResume, CoordinationDone} {
		target := claim.RunID
		if action == CoordinationResume || action == CoordinationDone {
			target = created.Data.Item.ID
		}
		_, err := client.MutateCoordination(t.Context(), ".", action, CoordinationMutationRequest{
			Target: target, Context: claim.Context, Summary: "synthetic upgrade verification",
			RequestID: []string{"123e4567-e89b-42d3-a456-426614174403", "123e4567-e89b-42d3-a456-426614174404", "123e4567-e89b-42d3-a456-426614174405"}[index],
		})
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	state, err := client.QueryCoordination(t.Context(), ".", CoordinationTaskShow, CoordinationRequest{Target: created.Data.Item.ID})
	if err != nil {
		t.Fatal(err)
	}
	var item struct {
		State string `json:"state"`
	}
	if json.Unmarshal(state.Item, &item) != nil || item.State != "done" {
		t.Fatal("synthetic task did not finish")
	}
}
