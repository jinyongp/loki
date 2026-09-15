package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type TokenSource interface {
	Token(context.Context) (string, error)
}

type ClientConfig struct {
	APIVersion                 string
	Targets                    []string
	MaxResponseBytes, MaxPages int
}

type Client struct {
	Config ClientConfig
	HTTP   *http.Client
	Tokens TokenSource
	apiURL string
}

type FieldOption struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type IssueField struct {
	ID          int64         `json:"id"`
	NodeID      string        `json:"node_id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	DataType    string        `json:"data_type"`
	Options     []FieldOption `json:"options,omitempty"`
}

type IssueFieldValue struct {
	IssueFieldID       int64           `json:"issue_field_id"`
	IssueFieldName     string          `json:"issue_field_name"`
	NodeID             string          `json:"node_id"`
	DataType           string          `json:"data_type"`
	Value              json.RawMessage `json:"value"`
	SingleSelectOption *FieldOption    `json:"single_select_option,omitempty"`
	MultiSelectOptions []FieldOption   `json:"multi_select_options,omitempty"`
}

type Value struct {
	FieldID  int64    `json:"field_id"`
	DataType string   `json:"data_type"`
	Text     string   `json:"text,omitempty"`
	Number   float64  `json:"number,omitempty"`
	Options  []string `json:"options,omitempty"`
}

func (c *Client) ListFields(ctx context.Context, target string) ([]IssueField, error) {
	owner, _, err := c.target(target)
	if err != nil {
		return nil, err
	}
	var fields []IssueField
	if err = c.request(ctx, http.MethodGet, "/orgs/"+url.PathEscape(owner)+"/issue-fields", nil, http.StatusOK, &fields); err != nil {
		return nil, err
	}
	for _, field := range fields {
		if field.ID <= 0 || !validDataType(field.DataType) {
			return nil, errors.New("GitHub Issue Fields response is invalid")
		}
	}
	return fields, nil
}

func (c *Client) ListValues(ctx context.Context, target string, issue int64) ([]IssueFieldValue, error) {
	owner, repo, err := c.targetIssue(target, issue)
	if err != nil {
		return nil, err
	}
	all := []IssueFieldValue{}
	for page := 1; page <= c.Config.MaxPages; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/issue-field-values?per_page=100&page=%d", url.PathEscape(owner), url.PathEscape(repo), issue, page)
		var values []IssueFieldValue
		if err = c.request(ctx, http.MethodGet, path, nil, http.StatusOK, &values); err != nil {
			return nil, err
		}
		for _, value := range values {
			if err = validateResult(value); err != nil {
				return nil, err
			}
		}
		all = append(all, values...)
		if len(values) < 100 {
			return all, nil
		}
	}
	return nil, errors.New("GitHub Issue Fields page limit exceeded")
}

func (c *Client) AddValues(ctx context.Context, target string, issue int64, values []Value) ([]IssueFieldValue, error) {
	return c.writeValues(ctx, http.MethodPost, target, issue, values)
}
func (c *Client) SetValues(ctx context.Context, target string, issue int64, values []Value) ([]IssueFieldValue, error) {
	return c.writeValues(ctx, http.MethodPut, target, issue, values)
}
func (c *Client) writeValues(ctx context.Context, method, target string, issue int64, values []Value) ([]IssueFieldValue, error) {
	owner, repo, err := c.targetIssue(target, issue)
	if err != nil {
		return nil, err
	}
	body, err := encodeValues(values)
	if err != nil {
		return nil, err
	}
	var result []IssueFieldValue
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/issue-field-values", url.PathEscape(owner), url.PathEscape(repo), issue)
	if err = c.request(ctx, method, path, body, http.StatusOK, &result); err != nil {
		return nil, err
	}
	for _, value := range result {
		if err = validateResult(value); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (c *Client) ClearValue(ctx context.Context, target string, issue, fieldID int64) error {
	owner, repo, err := c.targetIssue(target, issue)
	if err != nil {
		return err
	}
	if fieldID <= 0 {
		return errors.New("invalid GitHub issue field ID")
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/issue-field-values/%d", url.PathEscape(owner), url.PathEscape(repo), issue, fieldID)
	return c.request(ctx, http.MethodDelete, path, nil, http.StatusNoContent, nil)
}

func encodeValues(values []Value) ([]byte, error) {
	if len(values) == 0 || len(values) > 25 {
		return nil, errors.New("GitHub issue field values must contain 1 to 25 items")
	}
	items := make([]map[string]any, 0, len(values))
	ids := []int64{}
	for _, value := range values {
		if value.FieldID <= 0 || slices.Contains(ids, value.FieldID) {
			return nil, errors.New("invalid or duplicate GitHub issue field ID")
		}
		ids = append(ids, value.FieldID)
		var encoded any
		switch value.DataType {
		case "text", "single_select":
			if value.Text == "" || len(value.Text) > 8192 {
				return nil, errors.New("invalid GitHub issue field text value")
			}
			encoded = value.Text
		case "date":
			if _, err := time.Parse("2006-01-02", value.Text); err != nil {
				return nil, errors.New("invalid GitHub issue field date value")
			}
			encoded = value.Text
		case "number":
			if math.IsNaN(value.Number) || math.IsInf(value.Number, 0) {
				return nil, errors.New("invalid GitHub issue field number value")
			}
			encoded = value.Number
		case "multi_select":
			if len(value.Options) == 0 || len(value.Options) > 25 {
				return nil, errors.New("invalid GitHub issue field options")
			}
			seen := []string{}
			for _, option := range value.Options {
				if option == "" || len(option) > 200 || slices.Contains(seen, option) {
					return nil, errors.New("invalid GitHub issue field options")
				}
				seen = append(seen, option)
			}
			encoded = value.Options
		default:
			return nil, errors.New("unsupported GitHub issue field data type")
		}
		items = append(items, map[string]any{"field_id": value.FieldID, "value": encoded})
	}
	body, err := json.Marshal(map[string]any{"issue_field_values": items})
	if err != nil || len(body) > 65536 {
		return nil, errors.New("GitHub Issue Fields request is too large")
	}
	return body, nil
}

func (c *Client) target(target string) (string, string, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	if !slices.Contains(c.Config.Targets, target) {
		return "", "", errors.New("GitHub repository target is not allowed")
	}
	owner, repo, ok := strings.Cut(target, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return "", "", errors.New("invalid GitHub repository target")
	}
	return owner, repo, nil
}
func (c *Client) targetIssue(target string, issue int64) (string, string, error) {
	if issue <= 0 {
		return "", "", errors.New("invalid GitHub issue number")
	}
	return c.target(target)
}
func validDataType(value string) bool {
	return slices.Contains([]string{"text", "single_select", "number", "date", "multi_select"}, value)
}
func validateResult(value IssueFieldValue) error {
	if value.IssueFieldID <= 0 || !validDataType(value.DataType) || !json.Valid(value.Value) {
		return errors.New("GitHub Issue Fields response is invalid")
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, path string, body []byte, status int, out any) error {
	if c.HTTP == nil || c.Tokens == nil || c.Config.APIVersion != "2026-03-10" || c.Config.MaxResponseBytes < 4096 || c.Config.MaxResponseBytes > 16777216 || c.Config.MaxPages < 1 || c.Config.MaxPages > 100 {
		return errors.New("GitHub Issue Fields client is not configured")
	}
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return errors.New("GitHub authentication is unavailable")
	}
	base := c.apiURL
	if base == "" {
		base = defaultAPIURL
	}
	requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, method, strings.TrimRight(base, "/")+path, bytes.NewReader(body))
	if err != nil {
		return errors.New("GitHub Issue Fields request failed")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "loki")
	req.Header.Set("X-GitHub-Api-Version", c.Config.APIVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := *c.HTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	token = ""
	if err != nil {
		return errors.New("GitHub Issue Fields request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != status {
		return errors.New("GitHub Issue Fields request was rejected")
	}
	if out == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.Config.MaxResponseBytes)+1))
	if err != nil || len(data) > c.Config.MaxResponseBytes {
		return errors.New("GitHub Issue Fields response is invalid")
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(out) != nil {
		return errors.New("GitHub Issue Fields response is invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("GitHub Issue Fields response is invalid")
	}
	return nil
}
