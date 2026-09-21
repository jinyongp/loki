package githubapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"loki/internal/fault"
)

type ProviderConfig struct {
	APIVersion       string
	Targets          []string
	MaxResponseBytes int
	MaxPages         int
}

type Provider struct {
	Config ProviderConfig
	HTTP   *http.Client
	Tokens RepositoryTokenSource
	apiURL string
}

type ProviderReadAction string

const (
	ProviderReadRepository  ProviderReadAction = "repository"
	ProviderReadIssue       ProviderReadAction = "issue"
	ProviderReadPullRequest ProviderReadAction = "pull_request"
)

type ProviderReadRequest struct {
	Target string
	Action ProviderReadAction
	Number int64
}

type ProviderReadResult struct {
	Target      string
	Action      ProviderReadAction
	Repository  map[string]any
	Issue       map[string]any
	PullRequest map[string]any
}

type CommentRequest struct {
	Target    string
	Number    int64
	Body      string
	RequestID string
}

type CommentResult struct {
	Target      string
	Number      int64
	CommentID   int64
	HTMLURL     string
	RequestID   string
	OperationID string
	Replayed    bool
}

var providerRequestIDPattern = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")

func (p *Provider) Read(ctx context.Context, request ProviderReadRequest) (ProviderReadResult, error) {
	owner, repo, target, err := p.target(request.Target)
	if err != nil {
		return ProviderReadResult{}, err
	}
	result := ProviderReadResult{Target: target, Action: request.Action}
	switch request.Action {
	case ProviderReadRepository:
		path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
		var raw map[string]any
		if err = p.request(ctx, target, http.MethodGet, path, nil, http.StatusOK, &raw); err != nil {
			return ProviderReadResult{}, err
		}
		fullName, _ := raw["full_name"].(string)
		defaultBranch, _ := raw["default_branch"].(string)
		htmlURL, _ := raw["html_url"].(string)
		if fullName != target || defaultBranch == "" || htmlURL == "" {
			return ProviderReadResult{}, errors.New("GitHub repository response is invalid")
		}
		result.Repository = map[string]any{
			"full_name":      fullName,
			"default_branch": defaultBranch,
			"visibility":     stringValue(raw["visibility"]),
			"archived":       boolValue(raw["archived"]),
			"html_url":       htmlURL,
		}
	case ProviderReadIssue:
		if request.Number <= 0 {
			return ProviderReadResult{}, errors.New("GitHub issue number must be positive")
		}
		path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), request.Number)
		var raw map[string]any
		if err = p.request(ctx, target, http.MethodGet, path, nil, http.StatusOK, &raw); err != nil {
			return ProviderReadResult{}, err
		}
		number := int64Value(raw["number"])
		title, _ := raw["title"].(string)
		htmlURL, _ := raw["html_url"].(string)
		if number != request.Number || title == "" || htmlURL == "" {
			return ProviderReadResult{}, errors.New("GitHub issue response is invalid")
		}
		result.Issue = map[string]any{
			"number": number, "title": title, "state": stringValue(raw["state"]),
			"html_url": htmlURL, "is_pull_request": raw["pull_request"] != nil,
		}
	case ProviderReadPullRequest:
		if request.Number <= 0 {
			return ProviderReadResult{}, errors.New("GitHub pull request number must be positive")
		}
		path := fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(repo), request.Number)
		var raw map[string]any
		if err = p.request(ctx, target, http.MethodGet, path, nil, http.StatusOK, &raw); err != nil {
			return ProviderReadResult{}, err
		}
		number := int64Value(raw["number"])
		title, _ := raw["title"].(string)
		htmlURL, _ := raw["html_url"].(string)
		if number != request.Number || title == "" || htmlURL == "" {
			return ProviderReadResult{}, errors.New("GitHub pull request response is invalid")
		}
		result.PullRequest = map[string]any{
			"number": number, "title": title, "state": stringValue(raw["state"]),
			"html_url": htmlURL, "draft": boolValue(raw["draft"]),
		}
	default:
		return ProviderReadResult{}, errors.New("unsupported GitHub provider read action")
	}
	return result, nil
}

func (p *Provider) Comment(ctx context.Context, request CommentRequest) (CommentResult, error) {
	owner, repo, target, err := p.target(request.Target)
	if err != nil {
		return CommentResult{}, err
	}
	request.RequestID = strings.ToLower(strings.TrimSpace(request.RequestID))
	if !providerRequestIDPattern.MatchString(request.RequestID) {
		return CommentResult{}, fault.New(fault.CodeInvalidInput, "GitHub write request_id must be a UUID", false, "generate a new UUID request_id")
	}
	if request.Number <= 0 {
		return CommentResult{}, fault.New(fault.CodeInvalidInput, "GitHub issue or pull request number must be positive", false, "provide a positive issue or pull request number")
	}
	request.Body = strings.TrimSpace(request.Body)
	if request.Body == "" || len(request.Body) > 60000 {
		return CommentResult{}, fault.New(fault.CodeInvalidInput, "GitHub comment body must contain 1 to 60000 bytes", false, "provide a smaller non-empty comment body")
	}
	operationID := providerOperationID("comment", target, request.Number, request.RequestID)
	fingerprint := providerCommentFingerprint(request.Body)
	markerPrefix := "<!-- loki-operation:" + operationID + ":"
	marker := markerPrefix + fingerprint + " -->"
	existing, err := p.findComment(ctx, target, owner, repo, request.Number, markerPrefix, marker)
	if err != nil {
		return CommentResult{}, err
	}
	if existing != nil {
		return CommentResult{Target: target, Number: request.Number, CommentID: int64Value(existing["id"]), HTMLURL: stringValue(existing["html_url"]), RequestID: request.RequestID, OperationID: operationID, Replayed: true}, nil
	}
	body, _ := json.Marshal(map[string]string{"body": request.Body + "\n\n" + marker})
	var created map[string]any
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(repo), request.Number)
	if err = p.request(ctx, target, http.MethodPost, path, body, http.StatusCreated, &created); err != nil {
		detail := fault.Describe(err)
		if detail.Code == fault.CodeUnavailable {
			return CommentResult{}, fault.WithCorrelation(
				fault.New(fault.CodeOutcomeUnknown, "GitHub comment outcome is unknown; retry with the same request_id after inspecting upstream comments", true, "retry the identical comment request with the same request_id or inspect upstream comments for the Loki operation marker"),
				operationID,
			)
		}
		return CommentResult{}, err
	}
	commentID := int64Value(created["id"])
	htmlURL := stringValue(created["html_url"])
	commentBody := stringValue(created["body"])
	if commentID <= 0 || htmlURL == "" || !strings.Contains(commentBody, marker) {
		return CommentResult{}, fault.WithCorrelation(
			fault.New(fault.CodeOutcomeUnknown, "GitHub returned an invalid comment result; inspect upstream comments before retrying", false, "inspect upstream comments for the Loki operation marker"),
			operationID,
		)
	}
	return CommentResult{Target: target, Number: request.Number, CommentID: commentID, HTMLURL: htmlURL, RequestID: request.RequestID, OperationID: operationID}, nil
}

func (p *Provider) findComment(ctx context.Context, target, owner, repo string, number int64, markerPrefix, marker string) (map[string]any, error) {
	maxPages := p.Config.MaxPages
	if maxPages == 0 {
		maxPages = 10
	}
	for page := 1; page <= maxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", url.PathEscape(owner), url.PathEscape(repo), number, page)
		var comments []map[string]any
		if err := p.request(ctx, target, http.MethodGet, path, nil, http.StatusOK, &comments); err != nil {
			return nil, err
		}
		for _, comment := range comments {
			if int64Value(comment["id"]) <= 0 || stringValue(comment["html_url"]) == "" {
				return nil, errors.New("GitHub comment response is invalid")
			}
			body := stringValue(comment["body"])
			if strings.Contains(body, marker) {
				return comment, nil
			}
			if strings.Contains(body, markerPrefix) {
				return nil, fault.New(fault.CodeConflict, "GitHub write request_id was already used for different comment content", false, "generate a new request_id when the comment body changes")
			}
		}
		if len(comments) < 100 {
			return nil, nil
		}
	}
	return nil, errors.New("GitHub comment replay scan exceeded its page limit")
}

func providerCommentFingerprint(body string) string {
	sum := sha256.Sum256([]byte("loki-github-comment:v1\n" + body + "\n"))
	return hex.EncodeToString(sum[:])
}

func providerOperationID(action, target string, number int64, requestID string) string {
	sum := sha256.Sum256([]byte("loki-github-provider:v1\n" + action + "\n" + target + "\n" + strconv.FormatInt(number, 10) + "\n" + requestID + "\n"))
	return hex.EncodeToString(sum[:16])
}

func (p *Provider) target(raw string) (string, string, string, error) {
	if p == nil || p.HTTP == nil || p.Tokens == nil || p.Config.APIVersion != "2026-03-10" || p.Config.MaxResponseBytes < 4096 || p.Config.MaxResponseBytes > 16<<20 || p.Config.MaxPages < 0 || p.Config.MaxPages > 100 {
		return "", "", "", errors.New("GitHub provider is not configured")
	}
	target := strings.ToLower(strings.TrimSpace(raw))
	if !slices.Contains(p.Config.Targets, target) {
		return "", "", "", errors.New("GitHub repository target is not allowed")
	}
	owner, repo, ok := strings.Cut(target, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", "", errors.New("invalid GitHub repository target")
	}
	return owner, repo, target, nil
}

func (p *Provider) request(ctx context.Context, target, method, path string, body []byte, status int, out any) error {
	token, err := p.Tokens.Token(ctx, target)
	if err != nil || token == "" {
		return fault.New(fault.CodeUnavailable, "GitHub authentication is unavailable", true, "verify the configured GitHub App installation and retry")
	}
	base := p.apiURL
	if base == "" {
		base = defaultAPIURL
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("GitHub provider request failed")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "loki")
	req.Header.Set("X-GitHub-Api-Version", p.Config.APIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := *p.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	token = ""
	if err != nil {
		return fault.New(fault.CodeUnavailable, "GitHub provider request failed", true, "retry after checking GitHub connectivity")
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		return fault.New(fault.CodeFailed, "GitHub provider request was rejected", false, "inspect the configured repository and provider permissions")
	}
	if out == nil {
		return nil
	}
	limit := int64(p.Config.MaxResponseBytes)
	payload, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(payload)) > limit {
		return errors.New("GitHub provider response exceeds its size limit")
	}
	if err = json.Unmarshal(payload, out); err != nil {
		return errors.New("GitHub provider response is invalid")
	}
	return nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func int64Value(value any) int64 {
	number, _ := value.(float64)
	return int64(number)
}
