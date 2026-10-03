package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"loki/internal/fault"
	"loki/internal/process"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type ProjectAuthority struct {
	Targets                   []string
	AccountTypes              map[string]string
	Repositories              RepositoryTokenSource
	Users                     *UserAuthorization
	PersonalProjects          bool
	AuthorizePersonalProjects func(context.Context) error
	HTTP                      *http.Client
	apiURL                    string
}

// Projects must be linked to the selected repository. Creation links the new
// project in the same mutation. Caller-controlled linking and copying are excluded.
var projectCommandFlags = map[string]string{
	"list":          "limit closed",
	"view":          "",
	"create":        "title",
	"edit":          "title description readme visibility",
	"close":         "undo",
	"delete":        "",
	"mark-template": "undo",
	"field-list":    "limit",
	"field-create":  "name data-type single-select-options",
	"field-delete":  "id",
	"item-list":     "limit",
	"item-create":   "title body",
	"item-add":      "url",
	"item-delete":   "id",
	"item-archive":  "id undo",
	"item-edit":     "id field-id project-id title body clear text number date single-select-option-id iteration-id",
}

func ProjectCommandCapabilities() []string {
	names := make([]string, 0, len(projectCommandFlags))
	for name := range projectCommandFlags {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type projectCommand struct {
	name   string
	flags  map[string]string
	number int
}

func parseProjectCommand(request CommandRequest) (projectCommand, error) {
	invalid := fault.Error("GitHub Projects command arguments are invalid or outside the configured scope")
	if len(request.Input) != 0 || len(request.Args) < 2 || len(request.Args) > MaxCommandArguments || request.Args[0] != "project" {
		return projectCommand{}, invalid
	}
	name := request.Args[1]
	flags, ok := projectCommandFlags[name]
	if !ok {
		return projectCommand{}, invalid
	}
	// gh's jq evaluator can read its process environment, including GH_TOKEN.
	// Return bounded JSON/text without caller-controlled output evaluators.
	allowed := map[string]bool{"format": true}
	for _, flag := range strings.Fields(flags) {
		allowed[flag] = true
	}
	parsed := projectCommand{name: name, flags: map[string]string{}}
	positionals, total := 0, 0
	for _, arg := range request.Args {
		total += len(arg)
		if arg == "" || len(arg) > MaxCommandArgumentBytes || strings.IndexByte(arg, 0) >= 0 || total > MaxCommandArgumentTotalBytes {
			return projectCommand{}, invalid
		}
	}
	for i := 2; i < len(request.Args); i++ {
		arg := request.Args[i]
		if !strings.HasPrefix(arg, "--") {
			number, err := strconv.ParseInt(arg, 10, 32)
			if err != nil || number <= 0 {
				return projectCommand{}, invalid
			}
			positionals++
			parsed.number = int(number)
			continue
		}
		flag, value, inline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		if !allowed[flag] {
			return projectCommand{}, invalid
		}
		if _, duplicate := parsed.flags[flag]; duplicate {
			return projectCommand{}, invalid
		}
		if flag == "undo" || flag == "clear" || flag == "closed" {
			if inline {
				return projectCommand{}, invalid
			}
			value = "true"
		} else if !inline {
			i++
			if i >= len(request.Args) {
				return projectCommand{}, invalid
			}
			value = request.Args[i]
		}
		if value == "" || flag == "format" && value != "json" {
			return projectCommand{}, invalid
		}
		if flag == "limit" {
			limit, err := strconv.Atoi(value)
			if err != nil || limit < 1 || limit > 100 {
				return projectCommand{}, invalid
			}
		}
		if flag == "url" {
			u, err := url.Parse(value)
			parts := strings.Split(strings.TrimPrefix(uPath(u), "/"), "/")
			if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
				len(parts) != 4 || !strings.EqualFold(parts[0]+"/"+parts[1], strings.TrimSpace(request.Target)) || (parts[2] != "issues" && parts[2] != "pull") {
				return projectCommand{}, invalid
			}
			if number, err := strconv.ParseInt(parts[3], 10, 64); err != nil || number <= 0 {
				return projectCommand{}, invalid
			}
		}
		parsed.flags[flag] = value
	}
	nodeOnly := name == "field-delete" || name == "item-edit"
	if name == "list" || name == "create" || nodeOnly {
		if positionals != 0 {
			return projectCommand{}, invalid
		}
	} else if positionals != 1 {
		return projectCommand{}, invalid
	}
	if nodeOnly || name == "item-delete" || name == "item-archive" {
		if !credentialText(parsed.flags["id"], 256) {
			return projectCommand{}, invalid
		}
	}
	if name == "item-edit" {
		draft := parsed.flags["title"] != "" || parsed.flags["body"] != ""
		if !draft && (!credentialText(parsed.flags["project-id"], 256) || !credentialText(parsed.flags["field-id"], 256)) {
			return projectCommand{}, invalid
		}
	}
	if name == "create" && strings.TrimSpace(parsed.flags["title"]) == "" {
		return projectCommand{}, invalid
	}
	return parsed, nil
}

func uPath(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Path
}

type projectExecution struct {
	arguments []string
	token     string
	result    *process.Result
}

func (p *ProjectAuthority) Prepare(ctx context.Context, request CommandRequest) (projectExecution, error) {
	command, err := parseProjectCommand(request)
	if err != nil {
		return projectExecution{}, err
	}
	target := strings.ToLower(strings.TrimSpace(request.Target))
	if p == nil || !TargetAllowed(p.Targets, target) || p.HTTP == nil {
		return projectExecution{}, fault.Error("GitHub Projects target is not allowed")
	}
	owner, _, _ := strings.Cut(target, "/")
	var token string
	switch p.AccountTypes[owner] {
	case "user":
		if p.PersonalProjects {
			if p.AuthorizePersonalProjects != nil {
				if err := p.AuthorizePersonalProjects(ctx); err != nil {
					return projectExecution{}, err
				}
			}
			if p.Users == nil {
				return projectExecution{}, fault.Error("GitHub personal Projects authorization is required; enable the optional personal-projects capability and authorize this account")
			}
			token, err = p.Users.AccountToken(ctx, owner)
		} else {
			if p.Repositories == nil {
				return projectExecution{}, fault.Error("GitHub Projects installation token is unavailable")
			}
			token, err = p.Repositories.Token(ctx, target)
		}
	case "organization":
		if p.Repositories == nil {
			return projectExecution{}, fault.Error("GitHub Projects installation token is unavailable")
		}
		token, err = p.Repositories.Token(ctx, target)
	default:
		return projectExecution{}, fault.Error("GitHub Projects owner is not allowed")
	}
	if err != nil || token == "" {
		if err == nil {
			err = fault.Error("GitHub Projects credential is unavailable")
		}
		return projectExecution{}, err
	}
	if p.AccountTypes[owner] == "organization" {
		if err = p.organizationProjects(ctx, token, owner); err != nil {
			return projectExecution{}, err
		}
	}
	repository, err := p.projectRepository(ctx, token, target)
	if err != nil {
		return projectExecution{}, err
	}
	if command.name == "list" || command.name == "create" {
		result, err := p.repositoryProjectResult(ctx, token, target, repository, command)
		return projectExecution{token: token, result: &result}, err
	}
	// ID-based mutations can bypass gh's --owner handling. Check each node's
	// actual project and owner before allowing the CLI to receive any token.
	var projectID string
	if command.number > 0 {
		projectID, err = p.numberProject(ctx, token, owner, command.number)
		if err != nil {
			return projectExecution{}, err
		}
		if err = p.projectLinked(ctx, token, projectID, repository.ID); err != nil {
			return projectExecution{}, err
		}
	}
	for _, flag := range []string{"id", "project-id", "field-id"} {
		id := command.flags[flag]
		if id == "" {
			continue
		}
		kind := "ProjectV2"
		if flag == "field-id" || command.name == "field-delete" {
			kind = "ProjectV2Field"
		} else if flag == "id" {
			kind = "ProjectV2Item"
			if command.name == "item-edit" && (command.flags["title"] != "" || command.flags["body"] != "") {
				kind = "DraftIssue"
			}
		}
		resolved, err := p.nodeProject(ctx, token, id, kind, owner)
		if err != nil || projectID != "" && resolved != projectID {
			return projectExecution{}, fault.Error("GitHub Projects node is outside the selected repository's project")
		}
		if projectID == "" {
			if err = p.projectLinked(ctx, token, resolved, repository.ID); err != nil {
				return projectExecution{}, err
			}
		}
		projectID = resolved
	}
	arguments := append([]string(nil), request.Args...)
	if command.name != "field-delete" && command.name != "item-edit" {
		arguments = append(arguments, "--owner", owner)
	}
	return projectExecution{arguments: arguments, token: token}, nil
}

func (p *ProjectAuthority) nodeProject(ctx context.Context, token, id, kind, owner string) (string, error) {
	if !credentialText(id, 256) {
		return "", fault.Error("invalid GitHub Projects node")
	}
	projectFields := "id owner { __typename ... on User { login } ... on Organization { login } }"
	fragment := "... on ProjectV2 { " + projectFields + " }"
	switch kind {
	case "ProjectV2Field":
		fragment = "... on ProjectV2FieldCommon { project { " + projectFields + " } }"
	case "ProjectV2Item":
		fragment = "... on ProjectV2Item { project { " + projectFields + " } }"
	case "DraftIssue":
		fragment = "... on DraftIssue { projectsV2(first: 2) { nodes { " + projectFields + " } pageInfo { hasNextPage } } }"
	}
	body, _ := json.Marshal(map[string]any{"query": "query($id: ID!) { node(id: $id) { __typename " + fragment + " } }", "variables": map[string]string{"id": id}})
	base := p.apiURL
	if base == "" {
		base = defaultAPIURL
	}
	var response struct {
		Data struct {
			Node struct {
				Type     string       `json:"__typename"`
				ID       string       `json:"id"`
				Owner    projectOwner `json:"owner"`
				Project  ownedProject `json:"project"`
				Projects struct {
					Nodes    []ownedProject `json:"nodes"`
					PageInfo struct {
						HasNextPage bool `json:"hasNextPage"`
					} `json:"pageInfo"`
				} `json:"projectsV2"`
			} `json:"node"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	err := authorizationBodyJSON(ctx, p.HTTP, http.MethodPost, strings.TrimRight(base, "/")+"/graphql", token, bytes.NewReader(body), "application/json", &response)
	node := response.Data.Node
	if err != nil || len(response.Errors) != 0 || node.Type != kind && !(kind == "ProjectV2Field" && (node.Type == "ProjectV2SingleSelectField" || node.Type == "ProjectV2IterationField")) {
		return "", fault.Error("GitHub Projects node could not be verified")
	}
	project := node.Project
	if kind == "ProjectV2" {
		project = ownedProject{ID: node.ID, Owner: node.Owner}
	}
	if kind == "DraftIssue" {
		if len(node.Projects.Nodes) != 1 || node.Projects.PageInfo.HasNextPage {
			return "", fault.Error("GitHub draft issue must belong to one allowed project")
		}
		project = node.Projects.Nodes[0]
	}
	wantType := "User"
	if p.AccountTypes[owner] == "organization" {
		wantType = "Organization"
	}
	if project.ID == "" || project.Owner.Type != wantType || !strings.EqualFold(project.Owner.Login, owner) {
		return "", fault.Error("GitHub Projects owner does not match the selected target")
	}
	return project.ID, nil
}

type projectOwner struct {
	Type  string `json:"__typename"`
	Login string `json:"login"`
}

type ownedProject struct {
	ID     string       `json:"id"`
	Owner  projectOwner `json:"owner"`
	Number int          `json:"number,omitempty"`
	Title  string       `json:"title,omitempty"`
	URL    string       `json:"url,omitempty"`
	Closed bool         `json:"closed"`
}

func (p *ProjectAuthority) organizationProjects(ctx context.Context, token, owner string) error {
	base := defaultAPIURL
	if p.apiURL != "" {
		base = p.apiURL
	}
	var organization struct {
		Enabled *bool `json:"has_organization_projects"`
	}
	if err := authorizationJSON(ctx, p.HTTP, http.MethodGet, strings.TrimRight(base, "/")+"/orgs/"+owner, token, nil, &organization); err != nil || organization.Enabled == nil {
		return fault.Error("GitHub organization Projects availability could not be checked")
	}
	if !*organization.Enabled {
		return fault.Error("GitHub Projects are disabled for organization " + owner + "; enable 'Enable Projects for the organization' and Save at https://github.com/organizations/" + owner + "/settings/projects, then retry")
	}
	return nil
}
