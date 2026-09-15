package service

import (
	"context"
	"encoding/json"
	"testing"

	"loki/internal/githubapp"
)

type fakeIssueFields struct{ calls []string }

func (f *fakeIssueFields) ListFields(context.Context, string) ([]githubapp.IssueField, error) {
	f.calls = append(f.calls, "fields")
	return []githubapp.IssueField{{ID: 1, DataType: "text"}}, nil
}
func (f *fakeIssueFields) ListValues(context.Context, string, int64) ([]githubapp.IssueFieldValue, error) {
	f.calls = append(f.calls, "list")
	return []githubapp.IssueFieldValue{}, nil
}
func (f *fakeIssueFields) AddValues(context.Context, string, int64, []githubapp.Value) ([]githubapp.IssueFieldValue, error) {
	f.calls = append(f.calls, "add")
	return nil, nil
}
func (f *fakeIssueFields) SetValues(context.Context, string, int64, []githubapp.Value) ([]githubapp.IssueFieldValue, error) {
	f.calls = append(f.calls, "set")
	return nil, nil
}
func (f *fakeIssueFields) ClearValue(context.Context, string, int64, int64) error {
	f.calls = append(f.calls, "clear")
	return nil
}

func TestGitHubIssueFieldsRuntimeOperationsAndStrictInput(t *testing.T) {
	fake := &fakeIssueFields{}
	ops := GitHubIssueFieldsOperations(fake)
	cases := map[string]string{
		"github_fields_list":  `{"operation":"github_fields_list","target":"owner/repo"}`,
		"github_values_list":  `{"operation":"github_values_list","target":"owner/repo","issue":1}`,
		"github_values_add":   `{"operation":"github_values_add","target":"owner/repo","issue":1,"values":[{"field_id":1,"data_type":"text","text":"x"}]}`,
		"github_values_set":   `{"operation":"github_values_set","target":"owner/repo","issue":1,"values":[{"field_id":1,"data_type":"number","number":2}]}`,
		"github_values_clear": `{"operation":"github_values_clear","target":"owner/repo","issue":1,"field_id":1}`,
	}
	for name, raw := range cases {
		if _, err := ops[name].Handle(t.Context(), json.RawMessage(raw)); err != nil {
			t.Fatal(name, err)
		}
	}
	if len(fake.calls) != 5 {
		t.Fatal(fake.calls)
	}
	for _, field := range []string{"token", "url", "headers", "method"} {
		raw := json.RawMessage(`{"operation":"github_fields_list","target":"owner/repo","` + field + `":"private"}`)
		if _, err := ops["github_fields_list"].Handle(t.Context(), raw); err == nil {
			t.Fatal("accepted", field)
		}
	}
	if _, err := GitHubIssueFieldsOperations(nil)["github_fields_list"].Handle(t.Context(), json.RawMessage(`{"operation":"github_fields_list","target":"owner/repo"}`)); err == nil {
		t.Fatal("disabled GitHub accepted")
	}
}

type captureRuntime struct{ request map[string]any }

func (c *captureRuntime) Call(_ context.Context, value any) (json.RawMessage, error) {
	c.request = value.(map[string]any)
	return json.RawMessage(`{"fields":[]}`), nil
}
func TestGitHubIssueFieldsMCPBuildsNarrowRequest(t *testing.T) {
	runtime := &captureRuntime{}
	handler := GitHubIssueFieldsHandlers(runtime)["github_issue_fields"]
	result, err := handler(t.Context(), map[string]any{"action": "list_fields", "target": "owner/repo"})
	if err != nil || result == nil || runtime.request["operation"] != "github_fields_list" {
		t.Fatal(result, err, runtime.request)
	}
	if _, err = handler(t.Context(), map[string]any{"action": "request", "target": "owner/repo"}); err == nil {
		t.Fatal("arbitrary method accepted")
	}
}
