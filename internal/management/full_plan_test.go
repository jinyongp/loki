package management

import (
	"runtime"
	"slices"
	"testing"

	"loki/internal/tools"
)

func TestFullPlanKeepsPrivateExecutionAndSigningSelectionSeparate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("full deployment is a Linux host contract")
	}
	store := Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	target := LocalTarget(tools.Full)
	state.Installed["runtime-core"] = ownedFixtureTarget(t, store, target, "runtime-core")
	state.Installed["execution"] = ownedFixtureTarget(t, store, target, "execution", "runtime-core")
	git := ownedFixtureTarget(t, store, target, "git", "execution", "runtime-core")
	git.Manifest.Capabilities = []string{"signing"}
	state.Installed["git"] = git
	state.Config.Tools = []tools.Selection{{ID: "git", Enabled: true}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanFull()
	if err != nil {
		t.Fatal(err)
	}
	names := func(plan FullPlan) []string {
		var result []string
		for _, service := range plan.Services {
			result = append(result, service.Name)
		}
		return result
	}
	if !slices.Contains(names(plan), "executor") || slices.Contains(names(plan), "git-signing") || len(plan.Enabled) != 1 || plan.Enabled[0].ID != "git" {
		t.Fatalf("unexpected private/public services: %+v", plan)
	}
	if slices.Contains(names(plan), "endpoints") {
		t.Fatal("private Git execution acquired public host endpoint authority")
	}
	if _, err := plan.Program("execution", "../foreign"); err == nil {
		t.Fatal("program path escaped owned generation")
	}
	state.Config.Tools[0].Capabilities = []string{"signing"}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	plan, err = store.PlanFull()
	if err != nil || !slices.Contains(names(plan), "git-signing") {
		t.Fatalf("signing selection: %+v %v", plan, err)
	}
	state.Config.Tools[0].Enabled = false
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	plan, err = store.PlanFull()
	if err != nil || len(plan.Services) != 0 || len(plan.Programs) != 0 {
		t.Fatalf("disabled installations started services: %+v %v", plan, err)
	}
}
