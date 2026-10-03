package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"loki/internal/fault"
	"loki/internal/process"
)

// Bound scans when filtering closed boards or checking repository associations.
// An unfinished authority check fails closed.
const projectScopeMaxPages = 10

type projectRepository struct {
	ID    string `json:"id"`
	Owner struct {
		projectOwner
		ID string `json:"id"`
	} `json:"owner"`
}

type projectPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

const repositoryProjectFields = "id number title url closed owner { __typename ... on User { login } ... on Organization { login } }"

func (p *ProjectAuthority) projectGraphQL(ctx context.Context, token, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": variables})
	if err != nil {
		return fault.Error("GitHub repository Projects request is invalid")
	}
	base := defaultAPIURL
	if p.apiURL != "" {
		base = p.apiURL
	}
	var response struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err = authorizationBodyJSON(ctx, p.HTTP, http.MethodPost, strings.TrimRight(base, "/")+"/graphql", token, bytes.NewReader(body), "application/json", &response); err != nil || len(response.Errors) != 0 || len(response.Data) == 0 || string(response.Data) == "null" || json.Unmarshal(response.Data, out) != nil {
		return fault.Error("GitHub repository Projects request failed; check project permissions and inspect the repository's Projects before retrying a mutation")
	}
	return nil
}

func (p *ProjectAuthority) projectOwnerMatches(owner projectOwner, targetOwner string) bool {
	wantType := "User"
	if p.AccountTypes[targetOwner] == "organization" {
		wantType = "Organization"
	}
	return owner.Type == wantType && strings.EqualFold(owner.Login, targetOwner)
}

func (p *ProjectAuthority) projectRepository(ctx context.Context, token, target string) (projectRepository, error) {
	owner, name, _ := strings.Cut(target, "/")
	var response struct {
		Repository projectRepository `json:"repository"`
	}
	err := p.projectGraphQL(ctx, token, "query($owner: String!, $name: String!) { repository(owner: $owner, name: $name) { id owner { __typename id login } } }", map[string]any{"owner": owner, "name": name}, &response)
	repository := response.Repository
	if err != nil || repository.ID == "" || repository.Owner.ID == "" || !p.projectOwnerMatches(repository.Owner.projectOwner, owner) {
		return projectRepository{}, fault.Error("GitHub Projects repository could not be verified")
	}
	return repository, nil
}

func (p *ProjectAuthority) numberProject(ctx context.Context, token, owner string, number int) (string, error) {
	typeName := "user"
	if p.AccountTypes[owner] == "organization" {
		typeName = "organization"
	}
	query := "query($owner: String!, $number: Int!) { owner: " + typeName + "(login: $owner) { projectV2(number: $number) { id owner { __typename ... on User { login } ... on Organization { login } } } } }"
	var response struct {
		Owner struct {
			Project ownedProject `json:"projectV2"`
		} `json:"owner"`
	}
	err := p.projectGraphQL(ctx, token, query, map[string]any{"owner": owner, "number": number}, &response)
	project := response.Owner.Project
	if err != nil || project.ID == "" || !p.projectOwnerMatches(project.Owner, owner) {
		return "", fault.Error("GitHub Projects number could not be verified")
	}
	return project.ID, nil
}

func (p *ProjectAuthority) projectLinked(ctx context.Context, token, projectID, repositoryID string) error {
	var cursor any
	for page := 0; page < projectScopeMaxPages; page++ {
		var response struct {
			Node struct {
				Type         string `json:"__typename"`
				ID           string `json:"id"`
				Repositories struct {
					Nodes []struct {
						ID string `json:"id"`
					} `json:"nodes"`
					PageInfo projectPageInfo `json:"pageInfo"`
				} `json:"repositories"`
			} `json:"node"`
		}
		query := "query($id: ID!, $after: String) { node(id: $id) { __typename ... on ProjectV2 { id repositories(first: 100, after: $after) { nodes { id } pageInfo { hasNextPage endCursor } } } } }"
		if err := p.projectGraphQL(ctx, token, query, map[string]any{"id": projectID, "after": cursor}, &response); err != nil || response.Node.Type != "ProjectV2" || response.Node.ID != projectID {
			return fault.Error("GitHub Projects repository linkage could not be verified")
		}
		for _, repository := range response.Node.Repositories.Nodes {
			if repository.ID == repositoryID {
				return nil
			}
		}
		info := response.Node.Repositories.PageInfo
		if !info.HasNextPage {
			return fault.Error("GitHub Project is not linked to the selected repository")
		}
		if info.EndCursor == "" || info.EndCursor == cursor {
			break
		}
		cursor = info.EndCursor
	}
	return fault.Error("GitHub Projects repository linkage scan is incomplete; no operation was performed")
}

func (p *ProjectAuthority) repositoryProjectResult(ctx context.Context, token, target string, repository projectRepository, command projectCommand) (process.Result, error) {
	if command.name == "create" {
		var response struct {
			Created struct {
				Project ownedProject `json:"projectV2"`
			} `json:"createProjectV2"`
		}
		query := "mutation($input: CreateProjectV2Input!) { createProjectV2(input: $input) { projectV2 { " + repositoryProjectFields + " } } }"
		variables := map[string]any{"input": map[string]any{"ownerId": repository.Owner.ID, "repositoryId": repository.ID, "title": command.flags["title"]}}
		if err := p.projectGraphQL(ctx, token, query, variables, &response); err != nil {
			return process.Result{}, err
		}
		project := response.Created.Project
		if project.ID == "" || !p.projectOwnerMatches(project.Owner, repository.Owner.Login) {
			return process.Result{}, fault.Error("GitHub Project creation result could not be verified; inspect the repository's Projects before retrying")
		}
		output, _ := json.Marshal(project)
		return process.Result{Output: string(output)}, nil
	}
	limit := 30
	if value := command.flags["limit"]; value != "" {
		limit, _ = strconv.Atoi(value)
	}
	owner, name, _ := strings.Cut(target, "/")
	projects := make([]ownedProject, 0, limit)
	var cursor any
	total, complete := 0, false
	for page := 0; page < projectScopeMaxPages; page++ {
		var response struct {
			Repository struct {
				ID       string `json:"id"`
				Projects struct {
					Nodes      []ownedProject  `json:"nodes"`
					TotalCount int             `json:"totalCount"`
					PageInfo   projectPageInfo `json:"pageInfo"`
				} `json:"projectsV2"`
			} `json:"repository"`
		}
		query := "query($owner: String!, $name: String!, $after: String) { repository(owner: $owner, name: $name) { id projectsV2(first: 100, after: $after) { nodes { " + repositoryProjectFields + " } totalCount pageInfo { hasNextPage endCursor } } } }"
		if err := p.projectGraphQL(ctx, token, query, map[string]any{"owner": owner, "name": name, "after": cursor}, &response); err != nil || response.Repository.ID != repository.ID {
			return process.Result{}, fault.Error("GitHub repository Projects list could not be verified")
		}
		connection := response.Repository.Projects
		total = connection.TotalCount
		omitted := false
		for _, project := range connection.Nodes {
			if project.ID == "" || !p.projectOwnerMatches(project.Owner, owner) {
				return process.Result{}, fault.Error("GitHub repository Projects list contains an unverified project")
			}
			if project.Closed && command.flags["closed"] == "" {
				continue
			}
			if len(projects) == limit {
				omitted = true
				continue
			}
			projects = append(projects, project)
		}
		complete = !connection.PageInfo.HasNextPage && !omitted
		if len(projects) >= limit || !connection.PageInfo.HasNextPage {
			break
		}
		if connection.PageInfo.EndCursor == "" || connection.PageInfo.EndCursor == cursor {
			break
		}
		cursor = connection.PageInfo.EndCursor
	}
	output, _ := json.Marshal(map[string]any{"projects": projects, "totalCount": total, "complete": complete})
	return process.Result{Output: string(output)}, nil
}
