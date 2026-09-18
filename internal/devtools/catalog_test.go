package devtools

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestEmbeddedCatalogContainsOnlyApprovedCommands(t *testing.T) {
	commands, err := EmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != len(approvedNames) {
		t.Fatalf("got %d approved commands, want %d", len(commands), len(approvedNames))
	}
	for _, command := range commands {
		if !slices.Contains(approvedNames, command.Name) {
			t.Fatalf("unexpected approved command %q", command.Name)
		}
		if command.AcceptsChildArgs {
			t.Fatalf("approved command accepts child arguments: %q", command.Name)
		}
	}
	if !slices.Equal(approvedNames, []string{
		"command inspect", "command list", "process restart", "process start", "project inspect",
		"task checkpoint list", "task context", "task current", "task history", "task next", "task show",
		"task workstream context", "task workstream history", "task workstream list", "task workstream show",
	}) {
		t.Fatalf("runtime allowlist = %#v", approvedNames)
	}
	for _, denied := range []string{"doctor", "run", "secret set", "secret list", "import", "process logs", "project up", "task claim", "task takeover", "task checkpoint", "task release", "task done", "backup restore", "cleanup apply", "update"} {
		if slices.Contains(approvedNames, denied) {
			t.Fatalf("unsafe command approved: %q", denied)
		}
	}
}

func TestParseCatalogRejectsDrift(t *testing.T) {
	var envelope map[string]any
	if err := json.Unmarshal(embeddedCatalog, &envelope); err != nil {
		t.Fatal(err)
	}
	data := envelope["data"].(map[string]any)
	data["protocol_version"] = float64(2)
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ParseCatalog(raw); err == nil {
		t.Fatal("protocol drift accepted")
	}

	if _, err = ParseCatalog([]byte(`{"schema_version":1,"ok":false,"error":{"code":"failed","message":"no"}}`)); err == nil {
		t.Fatal("failure envelope accepted")
	}
}
