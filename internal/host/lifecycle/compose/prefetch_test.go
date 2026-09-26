package compose

import (
	"slices"
	"testing"

	"loki/internal/host/lifecycle"
)

func TestPrefetchPullsImmutableRequiredAndEnabledImagesWithoutRuntimeMutation(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.3.0", "e")
	installation := lifecycle.InstallationState{
		Scope: "user", Workspace: workspace, DockerAccess: "direct", MCPPort: lifecycle.DefaultMCPPort,
	}
	if err := backend.Prefetch(t.Context(), generation, installation, []string{"browser"}); err != nil {
		t.Fatal(err)
	}
	calls := runner.snapshot()
	want := [][]string{
		{"image", "pull", "ghcr.io/jinyongp/loki@sha256:" + repeatDigest("e")},
		{"image", "pull", "ghcr.io/jinyongp/loki-browser@sha256:" + repeatDigest("c")},
	}
	if len(calls) != len(want) {
		t.Fatalf("prefetch calls = %#v", calls)
	}
	for index := range want {
		if !slices.Equal(calls[index].args, want[index]) || len(calls[index].env) != 0 {
			t.Fatalf("prefetch call %d = %#v", index, calls[index])
		}
	}
	if _, found, err := backend.loadRuntime(); err != nil || found {
		t.Fatalf("prefetch mutated runtime state: found=%v err=%v", found, err)
	}
}

func TestPrefetchSkipsDisabledOptionalImages(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.3.0", "e")
	installation := lifecycle.InstallationState{
		Scope: "user", Workspace: workspace, DockerAccess: "direct", MCPPort: lifecycle.DefaultMCPPort,
	}
	if err := backend.Prefetch(t.Context(), generation, installation, nil); err != nil {
		t.Fatal(err)
	}
	calls := runner.snapshot()
	if len(calls) != 1 || !slices.Equal(calls[0].args, []string{
		"image", "pull", "ghcr.io/jinyongp/loki@sha256:" + repeatDigest("e"),
	}) {
		t.Fatalf("disabled optional prefetch calls = %#v", calls)
	}
}

func repeatDigest(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}
