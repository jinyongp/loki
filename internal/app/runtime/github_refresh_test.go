package runtime

import (
	"encoding/json"
	"testing"

	controlpolicy "loki/internal/control/policy"
	githubapp "loki/internal/integrations/github"
)

func TestGitHubRefreshIsHostOnlyAndRejectsExtraInput(t *testing.T) {
	operation := GitHubRefreshOperations(&githubapp.Broker{})["github_refresh"]
	if operation.Grant != controlpolicy.HostAdministration {
		t.Fatal("agents can clear installation credentials")
	}
	for _, raw := range []string{`{"token":"private"}`, `{"target":"other/repo"}`, `{} {}`} {
		if _, err := operation.Handle(t.Context(), json.RawMessage(raw)); err == nil {
			t.Fatal("refresh accepted arbitrary input", raw)
		}
	}
	result, err := operation.Handle(t.Context(), json.RawMessage(`{"operation":"github_refresh"}`))
	if err != nil || result.(map[string]any)["refreshed"] != true {
		t.Fatal("host refresh failed", result, err)
	}
	if _, err := GitHubRefreshOperations(nil)["github_refresh"].Handle(t.Context(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("refresh accepted a disabled integration")
	}
}
