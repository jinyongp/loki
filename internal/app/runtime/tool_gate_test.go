package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/config"
	"loki/internal/rpc"
	"loki/internal/tools"
)

func TestCachedRuntimeProviderOperationRechecksOptionalCapability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.json")
	configuration := tools.Config{Schema: 1, Release: "0.2.0", Host: tools.Host{Kind: "local"}, Mode: tools.Full, Tools: []tools.Selection{{ID: "github", Enabled: true, Capabilities: []string{"personal-projects"}}}}
	write := func() {
		t.Helper()
		data, _ := json.Marshal(configuration)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	gate := &config.ToolGate{Path: path, Release: "0.2.0", Mode: tools.Full}
	called := 0
	operations := gateOperations(gate, "personal-projects", map[string]rpc.Operation{"begin": {Handle: func(context.Context, json.RawMessage) (any, error) { called++; return "ok", nil }}})
	if _, err := operations["begin"].Handle(t.Context(), nil); err != nil || called != 1 {
		t.Fatalf("authorized call: %v", err)
	}
	configuration.Tools[0].Capabilities = nil
	write()
	if _, err := operations["begin"].Handle(t.Context(), nil); err == nil || called != 1 {
		t.Fatal("cached RPC touched personal credentials after capability was removed")
	}
	configuration.Tools[0].Enabled = false
	write()
	if _, err := operations["begin"].Handle(t.Context(), nil); err == nil || called != 1 {
		t.Fatal("disabled provider operation reached its implementation")
	}
}
