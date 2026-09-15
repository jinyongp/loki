package githubapp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type tokenFunc func(context.Context) (string, error)

func (f tokenFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

func clientFixture(t *testing.T, handler http.HandlerFunc) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler(w, r) }))
	t.Cleanup(server.Close)
	return &Client{Config: ClientConfig{APIVersion: "2026-03-10", Targets: []string{"owner/repo"}, MaxResponseBytes: 4096, MaxPages: 2}, HTTP: server.Client(), Tokens: tokenFunc(func(context.Context) (string, error) { return "installation-token", nil }), apiURL: server.URL}, &calls
}

func TestIssueFieldsOperations(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][]byte{}
	client, _ := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer installation-token" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("missing authentication headers")
		}
		key := r.Method + " " + r.URL.Path
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen[key] = body
		mu.Unlock()
		switch key {
		case "GET /orgs/owner/issue-fields":
			io.WriteString(w, `[{"id":1,"node_id":"n","name":"Priority","description":"","data_type":"single_select","options":[{"id":2,"name":"High","color":"red"}]}]`)
		case "GET /repos/owner/repo/issues/7/issue-field-values":
			io.WriteString(w, `[{"issue_field_id":1,"issue_field_name":"Priority","node_id":"n","data_type":"single_select","value":2,"single_select_option":{"id":2,"name":"High","color":"red"}}]`)
		case "POST /repos/owner/repo/issues/7/issue-field-values", "PUT /repos/owner/repo/issues/7/issue-field-values":
			io.WriteString(w, `[{"issue_field_id":1,"issue_field_name":"Priority","node_id":"n","data_type":"single_select","value":2}]`)
		case "DELETE /repos/owner/repo/issues/7/issue-field-values/1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s", key)
			w.WriteHeader(404)
		}
	})
	fields, err := client.ListFields(t.Context(), "OWNER/REPO")
	if err != nil || len(fields) != 1 || fields[0].DataType != "single_select" {
		t.Fatal(fields, err)
	}
	values, err := client.ListValues(t.Context(), "owner/repo", 7)
	if err != nil || len(values) != 1 {
		t.Fatal(values, err)
	}
	input := []Value{{FieldID: 1, DataType: "single_select", Text: "High"}, {FieldID: 2, DataType: "number", Number: 3.5}, {FieldID: 3, DataType: "date", Text: "2026-09-15"}, {FieldID: 4, DataType: "text", Text: "owner"}, {FieldID: 5, DataType: "multi_select", Options: []string{"A", "B"}}}
	if _, err = client.AddValues(t.Context(), "owner/repo", 7, input); err != nil {
		t.Fatal(err)
	}
	if _, err = client.SetValues(t.Context(), "owner/repo", 7, input); err != nil {
		t.Fatal(err)
	}
	if err = client.ClearValue(t.Context(), "owner/repo", 7, 1); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"POST", "PUT"} {
		var payload struct {
			Values []struct {
				FieldID int64 `json:"field_id"`
				Value   any   `json:"value"`
			} `json:"issue_field_values"`
		}
		if json.Unmarshal(seen[method+" /repos/owner/repo/issues/7/issue-field-values"], &payload) != nil || len(payload.Values) != 5 {
			t.Fatal("invalid mutation body")
		}
	}
}

func TestIssueFieldsRejectsInputsBeforeNetwork(t *testing.T) {
	client, calls := clientFixture(t, func(http.ResponseWriter, *http.Request) { t.Fatal("network reached") })
	for _, run := range []func() error{
		func() error { _, e := client.ListValues(t.Context(), "other/repo", 1); return e },
		func() error { _, e := client.ListValues(t.Context(), "owner/repo", 0); return e },
		func() error { return client.ClearValue(t.Context(), "owner/repo", 1, 0) },
		func() error { _, e := client.SetValues(t.Context(), "owner/repo", 1, nil); return e },
		func() error {
			_, e := client.SetValues(t.Context(), "owner/repo", 1, []Value{{FieldID: 1, DataType: "date", Text: "soon"}})
			return e
		},
		func() error {
			_, e := client.SetValues(t.Context(), "owner/repo", 1, []Value{{FieldID: 1, DataType: "multi_select", Options: []string{"A", "A"}}})
			return e
		},
	} {
		if err := run(); err == nil {
			t.Error("invalid input accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input reached network")
	}
}

func TestIssueFieldsBoundsStatusAndRedirect(t *testing.T) {
	for _, fixture := range []struct {
		status int
		body   string
	}{{500, "private-response-sentinel"}, {302, ""}, {200, strings.Repeat("x", 4097)}, {200, `[] trailing`}} {
		client, calls := clientFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if fixture.status == 302 {
				w.Header().Set("Location", "https://example.invalid/")
			}
			w.WriteHeader(fixture.status)
			io.WriteString(w, fixture.body)
		})
		_, err := client.ListFields(t.Context(), "owner/repo")
		if err == nil || strings.Contains(err.Error(), "private-response-sentinel") || calls.Load() != 1 {
			t.Fatalf("unsafe response: %v %d", err, calls.Load())
		}
	}
}

func TestIssueFieldsPageLimit(t *testing.T) {
	item := `{"issue_field_id":1,"issue_field_name":"T","node_id":"n","data_type":"text","value":"x"}`
	page := "[" + strings.TrimSuffix(strings.Repeat(item+",", 100), ",") + "]"
	client, calls := clientFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, page) })
	client.Config.MaxResponseBytes = 16384
	if _, err := client.ListValues(t.Context(), "owner/repo", 1); err == nil || calls.Load() != 2 {
		t.Fatalf("page limit not enforced: %v %d", err, calls.Load())
	}
}
