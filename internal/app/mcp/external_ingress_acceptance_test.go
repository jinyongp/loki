package mcpapp

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"loki/internal/config"
	"loki/internal/portguard"
)

type externalIngressTransport struct {
	token string
	host  string
}

func (t externalIngressTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+t.token)
	copy.Host = t.host
	return http.DefaultTransport.RoundTrip(copy)
}

func TestProviderNeutralExternalIngressAcceptance(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")

	origin := httptest.NewUnstartedServer(nil)
	defer origin.Close()
	_, portText, err := net.SplitHostPort(origin.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c.Port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	token := strings.Repeat("t", 43)
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) {
		return json.RawMessage(`{"in_use":false,"listeners":[],"initialized":true}`), nil
	})
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	const publicHost = "mcp.operator.example"
	app, err := NewMCP(c, MCPOptions{
		Runtime: runtime, PortGuard: runtime, Jobs: jobControllerFixture(),
		GitJobs: gitJobsFixture(t, c, nil), JobToolchains: emptyJobToolchainResolver{},
		Ports: ports, Policy: policyGenerationFixture(t), Token: token,
		IngressHosts: []string{publicHost},
		Environment:  map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	origin.Config.Handler = app
	origin.Start()

	target, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	defer proxy.Close()

	client, err := mcp.NewClient(
		&mcp.Implementation{Name: "provider-neutral-ingress-acceptance", Version: "1"},
		nil,
	).Connect(
		t.Context(),
		&mcp.StreamableClientTransport{
			Endpoint:   proxy.URL + "/mcp",
			HTTPClient: &http.Client{Transport: externalIngressTransport{token: token, host: publicHost}},
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if client.InitializeResult() == nil {
		t.Fatal("MCP initialize through generic ingress returned no result")
	}
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) == 0 {
		t.Fatalf("MCP tools/list through generic ingress = %#v err=%v", tools, err)
	}
	foundSystemInspect := false
	for _, tool := range tools.Tools {
		if tool.Name == "system_inspect" {
			foundSystemInspect = true
			break
		}
	}
	if !foundSystemInspect {
		t.Fatal("generic ingress MCP tools/list omitted system_inspect")
	}

	checkStatus := func(host, bearer string, want int) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, proxy.URL+"/mcp", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != want {
			t.Fatalf("generic ingress host=%q auth=%q status=%d want=%d", host, bearer, response.StatusCode, want)
		}
	}
	checkStatus("not-allowed.example", token, http.StatusMisdirectedRequest)
	checkStatus(publicHost, "wrong", http.StatusUnauthorized)
}
