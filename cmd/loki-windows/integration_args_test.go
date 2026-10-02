package main

import (
	"testing"
)

func TestPersonalProjectsInspectionIsExplicitAndGitHubOnly(t *testing.T) {
	for _, action := range []string{"status", "doctor"} {
		defaults, err := parseIntegrationAction(action, []string{"github"}, "loki-mcp")
		if err != nil || defaults.PersonalProjects {
			t.Fatal("default inspection enabled personal authorization", defaults, err)
		}
		selected, err := parseIntegrationAction(action, []string{"--personal-projects", "github"}, "loki-mcp")
		if err != nil || !selected.PersonalProjects {
			t.Fatal("explicit personal inspection lost", selected, err)
		}
	}
	for _, selection := range []struct{ action, name string }{{"list", ""}, {"status", "signing"}, {"doctor", "browser"}, {"enable", "github"}, {"remove", "github"}} {
		args := []string{"--personal-projects"}
		if selection.name != "" {
			args = append(args, selection.name)
		}
		if _, err := parseIntegrationAction(selection.action, args, "loki-mcp"); err == nil {
			t.Fatal("personal inspection accepted outside GitHub status/doctor", selection)
		}
	}
}
