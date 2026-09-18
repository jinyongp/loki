package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/portguard"
)

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(copy)
}

type accessFixture string

func (v accessFixture) Verify(token string) bool { return token == string(v) }
func TestAssembledMCPHTTPAndShutdown(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	server := httptest.NewUnstartedServer(nil)
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	c.Port, _ = strconv.Atoi(port)
	c.PublicHosts = []string{"mcp.example.test"}
	c.ArtifactBaseURL = "http://127.0.0.1:" + port + "/artifacts"
	c.PreviewBaseDomain = "preview.example.test"
	token := strings.Repeat("t", 43)
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) {
		return json.RawMessage(`{"in_use":false,"listeners":[],"initialized":true}`), nil
	})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "preview fixture") }))
	defer backend.Close()
	_, backendPortText, _ := net.SplitHostPort(backend.Listener.Addr().String())
	backendPort, _ := strconv.Atoi(backendPortText)
	guard := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
		if request.(map[string]any)["port"] == backendPort {
			return json.RawMessage(`{"in_use":true,"listeners":[{"command":"fixture"}]}`), nil
		}
		return runtime.Call(t.Context(), request)
	})
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"status": "running"}, nil
	})
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	generation := policyGenerationFixture(t)
	app, err := NewMCP(c, MCPOptions{Runtime: runtime, PortGuard: guard, Browser: browser, Ports: ports, Policy: generation, Token: token, Access: accessFixture("mcp-access"), PreviewAccess: accessFixture("preview-access"), Environment: map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server.Config.Handler = app
	server.Start()
	preview, err := app.Previews.Publish(map[string]int{"/": backendPort}, ".", "fixture", 900)
	if err != nil {
		t.Fatal(err)
	}
	previewURL, _ := url.Parse(preview["url"].(string))
	for _, assertion := range []string{"", "mcp-access", "preview-access"} {
		r := httptest.NewRequest("GET", previewURL.String(), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Cf-Access-Jwt-Assertion", assertion)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if assertion == "preview-access" {
			if w.Code != 200 || w.Body.String() != "preview fixture" {
				t.Fatal(w.Code, w.Body.String())
			}
		} else if w.Code != 401 {
			t.Fatal("MCP credential bypassed preview Access", w.Code)
		}
	}
	for _, test := range []struct {
		auth, host, origin, access string
		want                       int
	}{
		{"", "", "", "", 401}, {"wrong", "", "", "", 401}, {token, "other.example.test", "", "", 421}, {token, "", "https://other.example.test", "", 403}, {"", "", "", "preview-access", 401},
	} {
		r := httptest.NewRequest("POST", server.URL+"/mcp", strings.NewReader(`{}`))
		if test.auth != "" {
			r.Header.Set("Authorization", "Bearer "+test.auth)
		}
		if test.host != "" {
			r.Host = test.host
		}
		if test.origin != "" {
			r.Header.Set("Origin", test.origin)
		}
		if test.access != "" {
			r.Header.Set("Cf-Access-Jwt-Assertion", test.access)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		if w.Code != test.want {
			t.Fatal(test, w.Code, w.Body.String())
		}
	}
	client, err := mcp.NewClient(&mcp.Implementation{Name: "assembled-test", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if instructions := client.InitializeResult().Instructions; !strings.Contains(instructions, "devtools schema") || strings.Contains(instructions, "Use project for") {
		t.Fatal("stale MCP instructions", instructions)
	}
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 26 {
		t.Fatal(tools, err)
	}
	resources, err := client.ListResources(t.Context(), nil)
	if err != nil || len(resources.Resources) != 3 {
		t.Fatal(resources, err)
	}
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		r, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || r.IsError {
			encoded, _ := json.Marshal(r)
			t.Fatalf("%s: %v %s", name, err, encoded)
		}
		raw, _ := json.Marshal(r.StructuredContent)
		var result map[string]any
		if json.Unmarshal(raw, &result) != nil {
			t.Fatal(string(raw))
		}
		return result
	}
	call("workspace_edit", map[string]any{"action": "create", "path": "hello.txt", "content": "fixture"})
	if r := call("workspace_read", map[string]any{"action": "file", "path": "hello.txt"}); r["sha256"] == nil {
		t.Fatal(r)
	}
	serverInfo := call("system_inspect", map[string]any{"action": "server"})
	policyInfo := serverInfo["policy_generation"].(map[string]any)
	if policyInfo["sha256"] != generation.Digest() || policyInfo["schema"] != float64(1) {
		t.Fatalf("policy generation info = %#v", policyInfo)
	}
	call("browser_session", map[string]any{"action": "start"})
	call("secret_inspect", map[string]any{"action": "status"})
	shared := call("artifact_publish", map[string]any{"action": "file", "path": "hello.txt"})
	response, err := http.Get(shared["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !bytes.Equal(data, []byte("fixture")) {
		t.Fatal(response.StatusCode, err)
	}
	app.Close()
	app.Close()
	if len(app.Artifacts.List()) != 0 {
		t.Fatal("shares retained on shutdown")
	}
	w := httptest.NewRecorder()
	app.ServeHTTP(w, httptest.NewRequest("GET", server.URL+"/mcp", nil))
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
	if data, err := os.ReadFile(filepath.Join(c.Root, "hello.txt")); err != nil || string(data) != "fixture" {
		t.Fatal("workspace content not preserved", err)
	}
}

func TestNewMCPRequiresEffectivePolicyGeneration(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) { return map[string]any{}, nil })
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewMCP(c, MCPOptions{Runtime: runtime, PortGuard: runtime, Browser: browser, Ports: ports, Token: strings.Repeat("t", 43)}); err == nil || !strings.Contains(err.Error(), "effective policy generation") {
		t.Fatalf("missing policy generation error = %v", err)
	}
}

func TestNewMCPRequiresProtectedListenerPolicy(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) { return json.RawMessage(`{}`), nil })
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) { return map[string]any{}, nil })
	if _, err := NewMCP(c, MCPOptions{Runtime: runtime, PortGuard: runtime, Browser: browser, Policy: policyGenerationFixture(t), Token: strings.Repeat("t", 43)}); err == nil || !strings.Contains(err.Error(), "protected-port policy") {
		t.Fatalf("missing listener protection error = %v", err)
	}
}
