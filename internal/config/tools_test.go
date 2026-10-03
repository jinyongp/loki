package config

import "testing"

func TestToolSelectionPreservesManagementOnlyAndExplicitSubset(t *testing.T) {
	empty, err := ToolSelection([]string{})
	if err != nil || len(empty) != 0 {
		t.Fatal("empty selection acquired tools", empty, err)
	}
	selected, err := ToolSelection([]string{"workspace", "github"})
	if err != nil || len(selected) != 2 || !selected["workspace"] || !selected["github"] || selected["git"] || selected["secrets"] {
		t.Fatal("selected subset acquired unrelated tools", selected, err)
	}
	for _, names := range [][]string{{"github", "github"}, {"unknown"}} {
		if _, err := ToolSelection(names); err == nil {
			t.Fatal("invalid service collection accepted", names)
		}
	}
}
