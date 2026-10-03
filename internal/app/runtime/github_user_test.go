package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	controlpolicy "loki/internal/control/policy"
	githubapp "loki/modules/github"
)

func TestGitHubUserAuthorizationIsHostOnlyAndRejectsSecretInput(t *testing.T) {
	users := &githubapp.UserAuthorization{AppID: 123, Accounts: []string{"example-user"}, HTTP: http.DefaultClient,
		PrivateKey: func(context.Context) (string, error) { t.Fatal("status read private key"); return "", nil },
		Load:       func(context.Context) (string, error) { return "", nil }, Save: func(context.Context, string) error { return nil }}
	operations := GitHubUserOperations(users)
	for name, operation := range operations {
		if operation.Grant != controlpolicy.HostAdministration {
			t.Fatal("user authorization available to agents", name)
		}
		for _, raw := range []string{`{"account":"example-user","token":"ghu_private"}`, `{"account":"example-user","device_code":"private"}`, `{"account":"example-user","client_secret":"private"}`, `{} {}`} {
			if _, err := operation.Handle(t.Context(), json.RawMessage(raw)); err == nil {
				t.Fatal("secret or trailing input accepted", name)
			}
		}
	}
	result, err := operations["github_user_status"].Handle(t.Context(), json.RawMessage(`{"operation":"github_user_status","account":"example-user"}`))
	raw, _ := json.Marshal(result)
	if err != nil || !strings.Contains(string(raw), "unconfigured") || strings.Contains(string(raw), "token") {
		t.Fatal("invalid user status", result, err)
	}
	if _, err := GitHubUserOperations(nil)["github_user_status"].Handle(t.Context(), json.RawMessage(`{"account":"example-user"}`)); err == nil {
		t.Fatal("disabled integration accepted authorization")
	}
	result, err = operations["github_user_status"].Handle(t.Context(), json.RawMessage(`{}`))
	status, ok := result.(githubapp.UserAuthorizationView)
	if err != nil || !ok || status.Status != "unconfigured" || len(status.Accounts) != 1 || status.Accounts[0].Account != "example-user" {
		t.Fatal("accountless runtime status failed", result, err)
	}
}
