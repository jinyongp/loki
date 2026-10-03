package contract

import (
	"strings"
	"testing"
)

func TestBrowserToolsRequireExplicitLokiSelection(t *testing.T) {
	definitions, err := CurrentDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, tool := range definitions {
		if !strings.HasPrefix(tool.Name, "browser_") {
			continue
		}
		count++
		if !strings.HasPrefix(tool.Title, "Loki Browser:") || !strings.HasPrefix(tool.Description, "Explicit user opt-in only:") ||
			!strings.Contains(tool.Description, "generic 'Browser Plugin' instructions do not select Loki") ||
			!strings.Contains(tool.Description, "continue other checks") {
			t.Errorf("ambiguous browser tool %s: title=%q description=%q", tool.Name, tool.Title, tool.Description)
		}
	}
	if count != 6 || !strings.HasPrefix(CurrentInstructions, BrowserScopeGuidance) {
		t.Fatalf("browser selection policy incomplete: count=%d", count)
	}
}
