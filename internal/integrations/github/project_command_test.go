package githubapp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"loki/internal/fault"
	"loki/internal/process"
)

func personalProjectFixture(t *testing.T) (*CommandRunner, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	runner, repositoryCalls := commandRunnerFixture(t)
	var userCalls atomic.Int32
	stored, _ := json.Marshal(map[string]userCredential{"example-user": {AppID: 123, Account: "example-user", AccessToken: "ghu_personal", ClientID: "Iv1.example"}})
	users := &UserAuthorization{AppID: 123, Accounts: []string{"example-user"}, HTTP: http.DefaultClient,
		PrivateKey: func(context.Context) (string, error) { return "unused", nil },
		Load:       func(context.Context) (string, error) { userCalls.Add(1); return string(stored), nil },
		Save:       func(context.Context, string) error { return nil }}
	if _, ok := any(users).(RepositoryTokenSource); ok {
		t.Fatal("user authorization implements repository credentials")
	}
	runner.Projects = &ProjectAuthority{Targets: []string{"example-user/repo", "example-org/loki"}, AccountTypes: map[string]string{"example-user": "user", "example-org": "organization"},
		Repositories: runner.Tokens, Users: users, HTTP: http.DefaultClient}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/graphql" {
			var input projectFixtureRequest
			if json.NewDecoder(r.Body).Decode(&input) != nil || !projectFixtureResponse(w, input) {
				t.Error("unexpected repository Projects query")
			}
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/orgs/example-org" {
			t.Error("unexpected organization availability request", r.Method, r.URL.Path)
		}
		ioJSON(w, map[string]bool{"has_organization_projects": true})
	}))
	t.Cleanup(server.Close)
	runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
	runner.Supervisor = commandCheckSupervisor(func(_ context.Context, spec process.Spec) (process.Result, error) {
		values := map[string]string{}
		for _, entry := range spec.Env {
			name, value, _ := strings.Cut(entry, "=")
			values[name] = value
		}
		if values["GH_TOKEN"] == "" || values["GH_CONFIG_DIR"] == "" || values["GH_HOST"] != "github.com" {
			t.Error("Projects did not use the isolated CLI environment")
		}
		return process.Result{Output: strings.Join(spec.Argv[1:], "|") + "|" + values["GH_TOKEN"]}, nil
	})
	return runner, repositoryCalls, &userCalls
}

func TestOrganizationProjectsDisabledAdvicePreventsExecution(t *testing.T) {
	for _, scenario := range []string{"disabled", "enabled", "missing-setting", "upstream-error"} {
		t.Run(scenario, func(t *testing.T) {
			runner, _, userCalls := personalProjectFixture(t)
			var executed atomic.Int32
			runner.Supervisor = commandCheckSupervisor(func(context.Context, process.Spec) (process.Result, error) {
				executed.Add(1)
				return process.Result{}, nil
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/graphql" && scenario == "enabled" {
					var input projectFixtureRequest
					if json.NewDecoder(r.Body).Decode(&input) != nil || !projectFixtureResponse(w, input) {
						t.Error("unexpected repository Projects preflight")
					}
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/orgs/example-org" || r.Header.Get("Authorization") == "Bearer ghu_personal" {
					t.Error("incorrect organization preflight authority")
				}
				if scenario == "upstream-error" {
					w.WriteHeader(http.StatusForbidden)
					w.Write([]byte("private-upstream-detail"))
					return
				}
				if scenario == "missing-setting" {
					ioJSON(w, map[string]string{"login": "example-org"})
					return
				}
				ioJSON(w, map[string]bool{"has_organization_projects": scenario == "enabled"})
			}))
			defer server.Close()
			runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
			_, err := runner.Run(t.Context(), CommandRequest{Target: "example-org/loki", Args: []string{"project", "view", "1"}})
			if (err == nil) != (scenario == "enabled") || (executed.Load() == 1) != (scenario == "enabled") || userCalls.Load() != 0 {
				t.Fatal("organization preflight failed", err, executed.Load())
			}
			if scenario == "disabled" && (!strings.Contains(fault.Public(err), "https://github.com/organizations/example-org/settings/projects") || !strings.Contains(fault.Public(err), "Enable Projects for the organization")) {
				t.Fatal("disabled Projects did not provide enable instructions", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-upstream-detail") {
				t.Fatal("preflight exposed response details", err)
			}
		})
	}
}

func TestProjectsAuthorityUsesUserTokenOnlyForPersonalProjects(t *testing.T) {
	runner, repositoryCalls, userCalls := personalProjectFixture(t)
	result, err := runner.Run(t.Context(), CommandRequest{Target: "Example-User/Repo", Args: []string{"project", "create", "--title", "Example", "--format", "json"}})
	if err != nil || !strings.Contains(result.Output, "created-project") || strings.Contains(result.Output, "ghu_personal") || repositoryCalls.Load() != 0 || userCalls.Load() != 1 {
		t.Fatal("personal Projects authority or redaction failed", result, err)
	}
	result, err = runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: []string{"project", "view", "1"}})
	if err != nil || !strings.Contains(result.Output, "--owner|example-user") || !strings.Contains(result.Output, "[REDACTED]") || strings.Contains(result.Output, "ghu_personal") {
		t.Fatal("delegated Projects token was not redacted", result, err)
	}
	if _, err := runner.Run(t.Context(), CommandRequest{Target: "example-org/loki", Args: []string{"project", "list"}}); err != nil || repositoryCalls.Load() != 1 || userCalls.Load() != 2 {
		t.Fatal("organization Projects did not use the installation token", err)
	}
	if _, err := runner.Run(t.Context(), CommandRequest{Target: "example-org/loki", Args: []string{"api", "graphql"}}); err != nil || repositoryCalls.Load() != 2 || userCalls.Load() != 2 {
		t.Fatal("repository API commands acquired personal credentials", err)
	}
	runner.Projects.Users = nil
	if _, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: []string{"project", "list"}}); err == nil || repositoryCalls.Load() != 2 || fault.Public(err) != "GitHub personal Projects authorization is required; run integration setup github --personal-projects" {
		t.Fatal("personal Projects silently fell back to installation credentials")
	}
}

func TestProjectsRejectUnsafeCommandsBeforeCredentialAccess(t *testing.T) {
	runner, repositoryCalls, userCalls := personalProjectFixture(t)
	for _, args := range [][]string{
		{"project"}, {"project", "copy", "1"}, {"project", "link", "1", "--repo", "other/repo"},
		{"project", "create"}, {"project", "create", "--title", " "},
		{"project", "list", "--owner", "another-user"}, {"project", "list", "--owner=another-user"},
		{"project", "list", "--", "--owner", "another-user"}, {"project", "list", "-Rother/repo"},
		{"project", "list", "--format", "yaml"}, {"project", "list", "--web"},
		{"project", "list", "--format", "json", "--jq", "env.GH_TOKEN | @base64"},
		{"project", "list", "--format", "json", "--template", "{{.}}"},
		{"project", "list", "--limit", "101"}, {"project", "list", "--limit", "-1"},
		{"project", "list", "--limit", "1", "--limit", "2"},
		{"project", "create", "--body-file", "/etc/secret"}, {"project", "edit", "1", "--readme-file", "/etc/secret"},
		{"project", "view"}, {"project", "view", "0"}, {"project", "view", "1", "2"},
		{"project", "item-edit", "--id", "item-id", "--text", "changed"},
		{"project", "item-add", "1", "--url", "https://github.com/another-user/repo/issues/1"},
		{"project", "item-add", "1", "--url", "https://github.com/example-user/repo/issues/1?token=x"},
		{"project", "item-add", "1", "--url", "https://github.com@example.test/example-user/repo/issues/1"},
	} {
		if _, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: args}); err == nil {
			t.Error("accepted unsafe Projects command", args)
		}
	}
	if _, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: []string{"project", "list"}, Input: []byte("input")}); err == nil {
		t.Fatal("Projects accepted arbitrary input")
	}
	if _, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/other", Args: []string{"project", "list"}}); err == nil {
		t.Fatal("Projects accepted an unconfigured target")
	}
	if repositoryCalls.Load() != 0 || userCalls.Load() != 0 {
		t.Fatal("invalid Projects commands accessed credentials")
	}
}

func TestProjectsNodeMutationsVerifyOwnerAndSameProject(t *testing.T) {
	for _, scenario := range []string{"valid-item", "valid-draft", "valid-field", "wrong-owner", "wrong-type", "different-project", "graphql-error", "shared-draft"} {
		t.Run(scenario, func(t *testing.T) {
			runner, _, _ := personalProjectFixture(t)
			var executed atomic.Int32
			runner.Supervisor = commandCheckSupervisor(func(context.Context, process.Spec) (process.Result, error) {
				executed.Add(1)
				return process.Result{}, nil
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer ghu_personal" {
					t.Error("invalid node authorization")
				}
				var input struct {
					Query     string            `json:"query"`
					Variables map[string]string `json:"variables"`
				}
				if json.NewDecoder(r.Body).Decode(&input) != nil {
					t.Error("invalid node lookup")
				}
				vars := map[string]any{}
				for name, value := range input.Variables {
					vars[name] = value
				}
				if projectFixtureResponse(w, projectFixtureRequest{Query: input.Query, Variables: vars}) {
					return
				}
				id := input.Variables["id"]
				owner := "example-user"
				if scenario == "wrong-owner" {
					owner = "another-user"
				}
				project := ownedProject{ID: "project-id", Owner: projectOwner{Type: "User", Login: owner}}
				if scenario == "different-project" && id == "field-id" {
					project.ID = "other-project"
				}
				node := map[string]any{"__typename": "ProjectV2Item", "project": project}
				if id == "project-id" {
					node = map[string]any{"__typename": "ProjectV2", "id": project.ID, "owner": project.Owner}
				}
				if id == "field-id" {
					node["__typename"] = "ProjectV2SingleSelectField"
				}
				if scenario == "valid-draft" || scenario == "shared-draft" {
					projects := []ownedProject{project}
					if scenario == "shared-draft" {
						projects = append(projects, project)
					}
					node = map[string]any{"__typename": "DraftIssue", "projectsV2": map[string]any{"nodes": projects, "pageInfo": map[string]bool{"hasNextPage": false}}}
				}
				if scenario == "wrong-type" {
					node["__typename"] = "Issue"
				}
				response := map[string]any{"data": map[string]any{"node": node}}
				if scenario == "graphql-error" {
					response["errors"] = []any{map[string]any{"message": "ghu_personal"}}
				}
				ioJSON(w, response)
			}))
			defer server.Close()
			runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
			args := []string{"project", "item-edit", "--id", "item-id", "--field-id", "field-id", "--project-id", "project-id", "--text", "updated"}
			if scenario == "valid-draft" || scenario == "shared-draft" {
				args = []string{"project", "item-edit", "--id", "draft-id", "--title", "Updated draft"}
			}
			if scenario == "valid-field" {
				args = []string{"project", "field-delete", "--id", "field-id"}
			}
			_, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: args})
			valid := strings.HasPrefix(scenario, "valid-")
			if (err == nil) != valid || (executed.Load() == 1) != valid {
				t.Fatal("node ownership check failed", scenario, err)
			}
			if err != nil && strings.Contains(err.Error(), "ghu_personal") {
				t.Fatal("node error leaked user token")
			}
		})
	}
}
