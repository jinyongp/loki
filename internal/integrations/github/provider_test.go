package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"loki/internal/fault"
)

type providerTokenSource struct {
	token string
}

func (s providerTokenSource) Token(context.Context, string) (string, error) {
	if s.token == "" {
		return "", errors.New("missing token")
	}
	return s.token, nil
}

func providerFixture(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Provider{
		Config: ProviderConfig{
			APIVersion: "2026-03-10", Targets: []string{"owner/repo"},
			MaxResponseBytes: 1 << 20, MaxPages: 3,
		},
		HTTP: server.Client(), Tokens: providerTokenSource{token: "token"}, apiURL: server.URL,
	}
}

func TestProviderTypedReadsUseRepositoryScopedAPI(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	provider := providerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("provider omitted repository token")
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/repos/owner/repo":
			io.WriteString(w, `{"full_name":"owner/repo","default_branch":"main","visibility":"private","archived":false,"html_url":"https://github.com/owner/repo"}`)
		case "/repos/owner/repo/issues/7":
			io.WriteString(w, `{"number":7,"title":"issue","state":"open","html_url":"https://github.com/owner/repo/issues/7"}`)
		case "/repos/owner/repo/pulls/8":
			io.WriteString(w, `{"number":8,"title":"pr","state":"open","html_url":"https://github.com/owner/repo/pull/8","draft":true}`)
		default:
			t.Fatalf("unexpected provider path %s", r.URL.Path)
		}
	})
	for _, request := range []ProviderReadRequest{
		{Target: "owner/repo", Action: ProviderReadRepository},
		{Target: "owner/repo", Action: ProviderReadIssue, Number: 7},
		{Target: "owner/repo", Action: ProviderReadPullRequest, Number: 8},
	} {
		result, err := provider.Read(t.Context(), request)
		if err != nil || result.Target != "owner/repo" || result.Action != request.Action {
			t.Fatalf("provider read %#v = %#v, %v", request, result, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(paths, ",") != "/repos/owner/repo,/repos/owner/repo/issues/7,/repos/owner/repo/pulls/8" {
		t.Fatalf("provider paths = %#v", paths)
	}
	if _, err := provider.Read(t.Context(), ProviderReadRequest{Target: "other/repo", Action: ProviderReadRepository}); err == nil {
		t.Fatal("unconfigured repository target was accepted")
	}
}

func TestProviderCommentReplaysByOperationIdentity(t *testing.T) {
	var mu sync.Mutex
	var created map[string]any
	posts := 0
	provider := providerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/owner/repo/issues/9/comments"):
			mu.Lock()
			current := created
			mu.Unlock()
			if current == nil {
				io.WriteString(w, "[]")
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{current})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/issues/9/comments":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(body["body"], "<!-- loki-operation:") {
				t.Fatalf("comment marker missing: %q", body["body"])
			}
			mu.Lock()
			posts++
			created = map[string]any{
				"id": float64(77), "html_url": "https://github.com/owner/repo/issues/9#issuecomment-77",
				"body": body["body"],
			}
			current := created
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(current)
		default:
			t.Fatalf("unexpected provider request %s %s", r.Method, r.URL.String())
		}
	})
	request := CommentRequest{
		Target: "owner/repo", Number: 9, Body: "hello",
		RequestID: "123e4567-e89b-42d3-a456-426614174000",
	}
	first, err := provider.Comment(t.Context(), request)
	if err != nil || first.CommentID != 77 || first.Replayed || first.OperationID == "" {
		t.Fatalf("first comment = %#v, %v", first, err)
	}
	second, err := provider.Comment(t.Context(), request)
	if err != nil || second.CommentID != 77 || !second.Replayed || second.OperationID != first.OperationID {
		t.Fatalf("replayed comment = %#v, %v", second, err)
	}
	changed := request
	changed.Body = "changed"
	if _, err = provider.Comment(t.Context(), changed); err == nil {
		t.Fatal("changed comment reused request_id")
	} else if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
		t.Fatalf("changed comment error = %#v", detail)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatalf("comment POST count = %d", posts)
	}
}

type providerRoundTripper func(*http.Request) (*http.Response, error)

func (f providerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestProviderCommentReportsUncertainTransportOutcome(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: providerRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method == http.MethodGet {
			return &http.Response{
				StatusCode: http.StatusOK, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader("[]")), Request: request,
			}, nil
		}
		return nil, errors.New("connection reset")
	})}
	provider := &Provider{
		Config: ProviderConfig{
			APIVersion: "2026-03-10", Targets: []string{"owner/repo"},
			MaxResponseBytes: 1 << 20, MaxPages: 1,
		},
		HTTP: client, Tokens: providerTokenSource{token: "token"}, apiURL: "https://api.example.test",
	}
	_, err := provider.Comment(t.Context(), CommentRequest{
		Target: "owner/repo", Number: 1, Body: "hello",
		RequestID: "123e4567-e89b-42d3-a456-426614174001",
	})
	detail := fault.Describe(err)
	if detail.Code != fault.CodeOutcomeUnknown || detail.CorrelationID == "" || !detail.Retryable {
		t.Fatalf("uncertain comment detail = %#v, err=%v", detail, err)
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d", calls)
	}
}
