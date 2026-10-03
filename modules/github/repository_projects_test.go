package githubapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loki/internal/process"
)

type projectFixtureRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

func fixtureProject(owner, id string) ownedProject {
	typeName := "User"
	if owner == "example-org" {
		typeName = "Organization"
	}
	return ownedProject{ID: id, Number: 1, Title: "Linked board", Owner: projectOwner{Type: typeName, Login: owner}}
}

// Shared successful preflight responses; ownership-node tests handle their own
// node payloads so their rejected cases still exercise the authority checks.
func projectFixtureResponse(w http.ResponseWriter, input projectFixtureRequest) bool {
	owner, _ := input.Variables["owner"].(string)
	if owner == "" {
		owner = "example-user"
	}
	project := fixtureProject(owner, "project-id")
	switch {
	case strings.Contains(input.Query, "createProjectV2(input:"):
		values, _ := input.Variables["input"].(map[string]any)
		if values["ownerId"] == "org-id" {
			project = fixtureProject("example-org", "project-id")
		}
		project.ID = "created-project"
		project.Title, _ = values["title"].(string)
		ioJSON(w, map[string]any{"data": map[string]any{"createProjectV2": map[string]any{"projectV2": project}}})
	case strings.Contains(input.Query, "repository(owner:"):
		ownerID := "user-id"
		if owner == "example-org" {
			ownerID = "org-id"
		}
		repository := map[string]any{"id": "repo-id", "owner": map[string]any{"__typename": project.Owner.Type, "login": owner, "id": ownerID}}
		if strings.Contains(input.Query, "projectsV2(first:") {
			repository["projectsV2"] = map[string]any{"nodes": []any{}, "totalCount": 0, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}}
		}
		ioJSON(w, map[string]any{"data": map[string]any{"repository": repository}})
	case strings.Contains(input.Query, "repositories(first:"):
		ioJSON(w, map[string]any{"data": map[string]any{"node": map[string]any{"__typename": "ProjectV2", "id": input.Variables["id"], "repositories": map[string]any{"nodes": []any{map[string]any{"id": "repo-id"}}, "pageInfo": map[string]any{"hasNextPage": false}}}}})
	case strings.Contains(input.Query, "projectV2(number:"):
		ioJSON(w, map[string]any{"data": map[string]any{"owner": map[string]any{"projectV2": project}}})
	default:
		return false
	}
	return true
}

func TestRepositoryProjectsCreateUsesAtomicLinkAndSavedUserAuthority(t *testing.T) {
	for _, target := range []string{"example-user/repo", "example-org/loki"} {
		t.Run(target, func(t *testing.T) {
			runner, repositoryCalls, userCalls := personalProjectFixture(t)
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					ioJSON(w, map[string]bool{"has_organization_projects": true})
					return
				}
				wantToken := "Bearer ghu_personal"
				if strings.HasPrefix(target, "example-org/") {
					wantToken = "Bearer installation-token"
				}
				if r.Header.Get("Authorization") != wantToken {
					t.Error("wrong Projects credential")
				}
				var input projectFixtureRequest
				if json.NewDecoder(r.Body).Decode(&input) != nil {
					t.Fatal("invalid GraphQL request")
				}
				if strings.Contains(input.Query, "mutation") {
					mutations++
					values := input.Variables["input"].(map[string]any)
					wantOwner := "user-id"
					if strings.HasPrefix(target, "example-org/") {
						wantOwner = "org-id"
					}
					if values["ownerId"] != wantOwner || values["repositoryId"] != "repo-id" || values["title"] != "Board \"with quotes\"" || len(values) != 3 {
						t.Error("creation was not bound to the verified repository", values)
					}
				}
				if !projectFixtureResponse(w, input) {
					t.Error("unexpected query")
				}
			}))
			defer server.Close()
			runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
			runner.Supervisor = commandCheckSupervisor(func(context.Context, process.Spec) (process.Result, error) {
				t.Error("creation delegated to owner-wide gh project create")
				return process.Result{}, nil
			})
			result, err := runner.Run(t.Context(), CommandRequest{Target: target, Args: []string{"project", "create", "--title", "Board \"with quotes\"", "--format", "json"}})
			if err != nil || result.ExitCode != 0 || mutations != 1 || !strings.Contains(result.Output, "created-project") {
				t.Fatal("linked creation failed", result, err)
			}
			if strings.HasPrefix(target, "example-user/") && (userCalls.Load() != 1 || repositoryCalls.Load() != 0) {
				t.Fatal("personal authority changed")
			}
			if strings.HasPrefix(target, "example-org/") && (userCalls.Load() != 0 || repositoryCalls.Load() != 1) {
				t.Fatal("organization authority changed")
			}
		})
	}
}

func TestRepositoryProjectsListFiltersClosedAndReportsIncomplete(t *testing.T) {
	for _, includeClosed := range []bool{false, true} {
		runner, _, _ := personalProjectFixture(t)
		pages := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input projectFixtureRequest
			json.NewDecoder(r.Body).Decode(&input)
			if !strings.Contains(input.Query, "projectsV2(first:") {
				projectFixtureResponse(w, input)
				return
			}
			if input.Variables["owner"] != "example-user" || input.Variables["name"] != "repo" || strings.Contains(input.Query, "user(login:") {
				t.Error("list is not repository scoped")
			}
			pages++
			project := fixtureProject("example-user", "closed-board")
			project.Closed = true
			info := map[string]any{"hasNextPage": true, "endCursor": "page-2"}
			if pages == 2 {
				if input.Variables["after"] != "page-2" {
					t.Error("closed board filtering skipped pagination")
				}
				project = fixtureProject("example-user", "open-board")
				info = map[string]any{"hasNextPage": false, "endCursor": nil}
			}
			ioJSON(w, map[string]any{"data": map[string]any{"repository": map[string]any{"id": "repo-id", "projectsV2": map[string]any{"nodes": []ownedProject{project}, "totalCount": 2, "pageInfo": info}}}})
		}))
		runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
		args := []string{"project", "list", "--limit", "1", "--format", "json"}
		if includeClosed {
			args = append(args, "--closed")
		}
		result, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: args})
		var output struct {
			Projects []ownedProject `json:"projects"`
			Complete bool           `json:"complete"`
			Total    int            `json:"totalCount"`
		}
		if err != nil || json.Unmarshal([]byte(result.Output), &output) != nil || len(output.Projects) != 1 || output.Total != 2 {
			t.Fatal("bad scoped list", result, err)
		}
		if includeClosed && (pages != 1 || output.Complete || output.Projects[0].ID != "closed-board") || !includeClosed && (pages != 2 || !output.Complete || output.Projects[0].ID != "open-board") {
			t.Fatal("bad pagination or completeness", output, pages)
		}
		server.Close()
	}
}

func TestRepositoryProjectsRejectUnlinkedNumberedAndIDOperations(t *testing.T) {
	commands := [][]string{{"project", "view", "1"}, {"project", "edit", "1", "--title", "Updated"}, {"project", "delete", "1"}, {"project", "field-delete", "--id", "field-id"}, {"project", "item-delete", "1", "--id", "item-id"}, {"project", "item-edit", "--id", "item-id", "--field-id", "field-id", "--project-id", "project-id", "--text", "Update"}}
	for _, args := range commands {
		t.Run(strings.Join(args[:2], "-"), func(t *testing.T) {
			runner, _, _ := personalProjectFixture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input projectFixtureRequest
				json.NewDecoder(r.Body).Decode(&input)
				if strings.Contains(input.Query, "repositories(first:") {
					ioJSON(w, map[string]any{"data": map[string]any{"node": map[string]any{"__typename": "ProjectV2", "id": input.Variables["id"], "repositories": map[string]any{"nodes": []any{map[string]any{"id": "other-repo-id"}}, "pageInfo": map[string]any{"hasNextPage": false}}}}})
					return
				}
				if projectFixtureResponse(w, input) {
					return
				}
				project := fixtureProject("example-user", "project-id")
				typeName := "ProjectV2Item"
				if args[1] == "field-delete" {
					typeName = "ProjectV2Field"
				}
				ioJSON(w, map[string]any{"data": map[string]any{"node": map[string]any{"__typename": typeName, "project": project}}})
			}))
			defer server.Close()
			runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
			runner.Supervisor = commandCheckSupervisor(func(context.Context, process.Spec) (process.Result, error) {
				t.Error("unlinked project reached the CLI")
				return process.Result{}, nil
			})
			if _, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: args}); err == nil || !strings.Contains(err.Error(), "not linked") {
				t.Fatal("accepted same-owner unrelated project", err)
			}
		})
	}
}

func TestRepositoryProjectLinkagePaginatesAndFailsClosed(t *testing.T) {
	for _, scenario := range []string{"later-page", "missing-cursor", "repeated-cursor", "scan-bound", "graphql-error"} {
		t.Run(scenario, func(t *testing.T) {
			runner, _, _ := personalProjectFixture(t)
			pages, executions := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input projectFixtureRequest
				json.NewDecoder(r.Body).Decode(&input)
				if !strings.Contains(input.Query, "repositories(first:") {
					projectFixtureResponse(w, input)
					return
				}
				pages++
				if scenario == "graphql-error" {
					ioJSON(w, map[string]any{"errors": []any{map[string]any{"message": "ghu_personal private-upstream-detail"}}})
					return
				}
				repoID, cursor, more := "other-id", fmt.Sprint(pages), true
				if scenario == "later-page" && pages == 2 {
					if input.Variables["after"] != "1" {
						t.Error("missing linkage cursor")
					}
					repoID, more = "repo-id", false
				}
				if scenario == "missing-cursor" {
					cursor = ""
				}
				if scenario == "repeated-cursor" {
					cursor = "same"
				}
				ioJSON(w, map[string]any{"data": map[string]any{"node": map[string]any{"__typename": "ProjectV2", "id": "project-id", "repositories": map[string]any{"nodes": []any{map[string]any{"id": repoID}}, "pageInfo": map[string]any{"hasNextPage": more, "endCursor": cursor}}}}})
			}))
			defer server.Close()
			runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
			runner.Supervisor = commandCheckSupervisor(func(context.Context, process.Spec) (process.Result, error) {
				executions++
				return process.Result{}, nil
			})
			_, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: []string{"project", "view", "1"}})
			if scenario == "later-page" {
				if err != nil || pages != 2 || executions != 1 {
					t.Fatal("later repository link rejected", err, pages)
				}
				return
			}
			if err == nil || executions != 0 || strings.Contains(err.Error(), "ghu_personal") || strings.Contains(err.Error(), "private-upstream-detail") {
				t.Fatal("incomplete authority check permitted execution or leaked details", err)
			}
			if scenario == "scan-bound" && pages != projectScopeMaxPages {
				t.Fatal("unbounded linkage scan", pages)
			}
		})
	}
}

func TestRepositoryProjectNumberAndItemMustMatch(t *testing.T) {
	runner, _, _ := personalProjectFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input projectFixtureRequest
		json.NewDecoder(r.Body).Decode(&input)
		if projectFixtureResponse(w, input) {
			return
		}
		project := fixtureProject("example-user", "other-project-id")
		ioJSON(w, map[string]any{"data": map[string]any{"node": map[string]any{"__typename": "ProjectV2Item", "project": project}}})
	}))
	defer server.Close()
	runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
	runner.Supervisor = commandCheckSupervisor(func(context.Context, process.Spec) (process.Result, error) {
		t.Error("item from another project reached CLI")
		return process.Result{}, nil
	})
	if _, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: []string{"project", "item-delete", "1", "--id", "item-id"}}); err == nil {
		t.Fatal("numbered project mismatch accepted")
	}
}

func TestRepositoryProjectCreateFailureDoesNotRetryOrExposeUpstream(t *testing.T) {
	for _, scenario := range []string{"http-error", "partial-graphql-error", "empty-result"} {
		t.Run(scenario, func(t *testing.T) {
			runner, repositoryCalls, _ := personalProjectFixture(t)
			mutations := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input projectFixtureRequest
				json.NewDecoder(r.Body).Decode(&input)
				if !strings.Contains(input.Query, "mutation") {
					projectFixtureResponse(w, input)
					return
				}
				mutations++
				if scenario == "http-error" {
					w.WriteHeader(http.StatusForbidden)
					w.Write([]byte("ghu_personal private-upstream-detail"))
					return
				}
				response := map[string]any{"data": map[string]any{"createProjectV2": nil}}
				if scenario == "partial-graphql-error" {
					response["data"] = map[string]any{"createProjectV2": map[string]any{"projectV2": fixtureProject("example-user", "created")}}
					response["errors"] = []any{map[string]any{"message": "ghu_personal private-upstream-detail"}}
				}
				ioJSON(w, response)
			}))
			defer server.Close()
			runner.Projects.HTTP, runner.Projects.apiURL = server.Client(), server.URL
			result, err := runner.Run(t.Context(), CommandRequest{Target: "example-user/repo", Args: []string{"project", "create", "--title", "Board"}})
			if err == nil || result.Output != "" || mutations != 1 || repositoryCalls.Load() != 0 || strings.Contains(err.Error(), "ghu_personal") || strings.Contains(err.Error(), "private-upstream-detail") {
				t.Fatal("creation failure leaked credentials, retried, or fell back", result, err, mutations)
			}
		})
	}
}
