package service

import (
	"context"
	"encoding/json"
	"testing"

	"loki/internal/integrations/github"
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
	switch c.request["operation"] {
	case "github_fields_list":
		return json.RawMessage(`{"fields":[]}`), nil
	case "github_values_clear":
		return json.RawMessage(`{"cleared":true}`), nil
	default:
		return json.RawMessage(`{"values":[]}`), nil
	}
}

func TestGitHubIssueFieldsMCPBuildsNarrowReadAndWriteRequests(t *testing.T) {
	runtime := &captureRuntime{}
	handlers := GitHubIssueFieldsHandlers(runtime)
	read := handlers["github_issue_fields_read"]
	write := handlers["github_issue_fields_write"]

	result, err := read(t.Context(), map[string]any{"action": "list_fields", "target": "owner/repo"})
	if err != nil || result == nil || runtime.request["operation"] != "github_fields_list" {
		t.Fatal(result, err, runtime.request)
	}
	result, err = read(t.Context(), map[string]any{"action": "list_values", "target": "owner/repo", "issue": 7})
	if err != nil || result == nil || runtime.request["operation"] != "github_values_list" || runtime.request["issue"] != int64(7) {
		t.Fatal(result, err, runtime.request)
	}
	result, err = write(t.Context(), map[string]any{
		"action": "add_values", "target": "owner/repo", "issue": 8,
		"values": []map[string]any{{"field_id": 1, "data_type": "text", "text": "x"}},
	})
	if err != nil || result == nil || runtime.request["operation"] != "github_values_add" || runtime.request["issue"] != int64(8) {
		t.Fatal(result, err, runtime.request)
	}
	result, err = write(t.Context(), map[string]any{
		"action": "clear_value", "target": "owner/repo", "issue": 9, "field_id": 3,
	})
	if err != nil || result == nil || runtime.request["operation"] != "github_values_clear" || runtime.request["field_id"] != int64(3) {
		t.Fatal(result, err, runtime.request)
	}

	if _, err = read(t.Context(), map[string]any{"action": "add_values", "target": "owner/repo"}); err == nil {
		t.Fatal("read handler accepted mutation action")
	}
	if _, err = write(t.Context(), map[string]any{"action": "list_fields", "target": "owner/repo"}); err == nil {
		t.Fatal("write handler accepted read action")
	}
}
