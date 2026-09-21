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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/portguard"
	"loki/internal/work/jobs"
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
	app, err := NewMCP(c, MCPOptions{Runtime: runtime, PortGuard: guard, Browser: browser, Jobs: jobControllerFixture(), GitJobs: gitJobsFixture(t, c, nil), JobToolchains: emptyJobToolchainResolver{}, Ports: ports, Policy: generation, Token: token, Access: accessFixture("mcp-access"), PreviewAccess: accessFixture("preview-access"), Environment: map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"}})
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
	if instructions := client.InitializeResult().Instructions; !strings.Contains(instructions, "project_coordination") ||
		!strings.Contains(instructions, "project_coordination_write") || !strings.Contains(instructions, "job action=start") ||
		strings.Contains(instructions, "task queues") {
		t.Fatal("stale MCP instructions", instructions)
	}
	tools, err := client.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 30 {
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
	if serverInfo["server_time"] == nil {
		t.Fatal("server time missing")
	}

	second, err := mcp.NewClient(&mcp.Implementation{Name: "assembled-test-second", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	secondResult, err := second.CallTool(t.Context(), &mcp.CallToolParams{Name: "system_inspect", Arguments: map[string]any{"action": "workspace"}})
	if err != nil || secondResult.IsError {
		t.Fatalf("second session call: %#v %v", secondResult, err)
	}

	activity := call("system_inspect", map[string]any{"action": "activity", "limit": 50})
	refs := map[string]bool{}
	for _, raw := range activity["items"].([]any) {
		item := raw.(map[string]any)
		if ref, _ := item["session_ref"].(string); ref != "" {
			if len(ref) != 16 {
				t.Fatalf("invalid session_ref %q", ref)
			}
			refs[ref] = true
		}
	}
	if len(refs) < 2 {
		t.Fatalf("activity did not distinguish MCP sessions: %#v", activity)
	}

	jobStart := call("job", map[string]any{
		"action": "start", "request_id": "123e4567-e89b-12d3-a456-426614174300",
		"argv": []string{"/bin/true"}, "timeout_seconds": 30,
	})
	jobID := jobStart["job_id"].(string)
	if jobStart["detached"] != true || jobStart["replayed"] != false {
		t.Fatalf("job start = %#v", jobStart)
	}
	if jobInspect := call("job", map[string]any{"action": "inspect", "job_id": jobID}); jobInspect["state"] != string(jobs.StateRunning) {
		t.Fatalf("job inspect = %#v", jobInspect)
	}
	if jobOutput := call("job", map[string]any{"action": "output", "job_id": jobID}); jobOutput["output"] != "live" {
		t.Fatalf("job output = %#v", jobOutput)
	}
	if jobCancel := call("job", map[string]any{"action": "cancel", "job_id": jobID}); jobCancel["canceled"] != true {
		t.Fatalf("job cancel = %#v", jobCancel)
	}

	call("browser_session", map[string]any{"action": "start"})
	call("secret_inspect", map[string]any{"action": "status"})
	shared := call("artifact_publish", map[string]any{
		"action": "file", "path": "hello.txt",
		"request_id": "79000000-0000-4000-8000-000000000010",
	})
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

func TestNewMCPExcludesDisabledIntegrationTools(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) {
		return json.RawMessage(`{"in_use":false,"listeners":[],"initialized":true}`), nil
	})
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewMCP(c, MCPOptions{
		Runtime: runtime, PortGuard: runtime, Jobs: jobControllerFixture(),
		GitJobs: gitJobsFixture(t, c, nil), JobToolchains: emptyJobToolchainResolver{},
		Ports: ports, Policy: policyGenerationFixture(t), Token: strings.Repeat("t", 43),
		Environment: map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := app.Server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "disabled-integrations-test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 19 {
		t.Fatalf("disabled integration tool count = %d, want 19", len(listed.Tools))
	}
	forbidden := map[string]bool{
		"browser_session": true, "browser_observe": true, "browser_interact": true,
		"browser_screenshot": true, "browser_save_screenshot": true, "browser_share_screenshot": true,
		"share_image": true, "artifact_publish": true,
		"preview_publish": true, "shared_resources": true, "revoke_share": true,
		"github": true, "github_issue_fields_read": true, "github_issue_fields_write": true,
	}
	for _, tool := range listed.Tools {
		if forbidden[tool.Name] {
			t.Fatalf("disabled integration tool was registered: %s", tool.Name)
		}
	}
}

func TestNewMCPRequiresExecutorJobClient(t *testing.T) {
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
	if _, err = NewMCP(c, MCPOptions{
		Runtime: runtime, PortGuard: runtime, Browser: browser,
		Ports: ports, Policy: policyGenerationFixture(t), Token: strings.Repeat("t", 43),
	}); err == nil || !strings.Contains(err.Error(), "executor Job clients") {
		t.Fatalf("missing executor Job client error = %v", err)
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
	if _, err := NewMCP(c, MCPOptions{Runtime: runtime, PortGuard: runtime, Browser: browser, Jobs: jobControllerFixture(), Ports: ports, Token: strings.Repeat("t", 43)}); err == nil || !strings.Contains(err.Error(), "effective policy generation") {
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
	if _, err := NewMCP(c, MCPOptions{Runtime: runtime, PortGuard: runtime, Browser: browser, Jobs: jobControllerFixture(), Policy: policyGenerationFixture(t), Token: strings.Repeat("t", 43)}); err == nil || !strings.Contains(err.Error(), "protected-port policy") {
		t.Fatalf("missing listener protection error = %v", err)
	}
}

func TestMCPTransportUsesBoundedStatefulSessions(t *testing.T) {
	options := mcpTransportOptions()
	if options.Stateless {
		t.Fatal("MCP transport is stateless")
	}
	if !options.JSONResponse || options.SessionTimeout != 24*time.Hour || options.MaxRequestBodyBytes != 16777216 {
		t.Fatalf("transport options = %#v", options)
	}
	if options.SessionTimeout <= 0 {
		t.Fatal("MCP session timeout is not bounded")
	}
}

func TestNewMCPAgentGuidanceUsesNativeProvider(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	repo := filepath.Join(c.Root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	gitInit := exec.CommandContext(t.Context(), "/usr/bin/git", "init", "-q", "--initial-branch=main")
	gitInit.Dir = repo
	gitInit.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if output, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("native rules\n"), 0644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(repo, ".agents", "skills", "project-skill")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: project-skill\ndescription: Project skill.\n---\n\n# Project skill\n"), 0644); err != nil {
		t.Fatal(err)
	}
	packagedRoot := t.TempDir()
	packagedSkill := filepath.Join(packagedRoot, "packaged-skill")
	if err := os.MkdirAll(packagedSkill, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagedSkill, "SKILL.md"), []byte("---\nname: packaged-skill\ndescription: Packaged skill.\n---\n\n# Packaged skill\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runtimeCalls := 0
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) {
		runtimeCalls++
		return json.RawMessage(`{}`), nil
	})
	browser := browserFixture(func(context.Context, string, map[string]any) (map[string]any, error) {
		return map[string]any{"status": "running"}, nil
	})
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	app, err := NewMCP(c, MCPOptions{
		Runtime: runtime, PortGuard: runtime, Browser: browser, Jobs: jobControllerFixture(),
		GitJobs: gitJobsFixture(t, c, nil), JobToolchains: emptyJobToolchainResolver{}, Ports: ports,
		Policy: policyGenerationFixture(t), Token: strings.Repeat("t", 43),
		PackagedSkillRoot: packagedRoot,
		Environment: map[string]string{
			"HOME": home, "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := app.Server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "native-agent-guidance-test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	result, err := client.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "agent_guidance",
		Arguments: map[string]any{
			"action": "context", "cwd": "repo", "target": "src/new.go",
		},
	})
	if err != nil || result.IsError {
		t.Fatalf("agent_guidance: result=%#v err=%v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	guidance, ok := payload["guidance"].(map[string]any)
	if !ok || guidance["target"] != "src/new.go" {
		t.Fatalf("guidance = %#v", payload["guidance"])
	}
	sources, ok := guidance["sources"].([]any)
	if !ok || len(sources) != 1 || sources[0].(map[string]any)["content"] != "native rules\n" {
		t.Fatalf("guidance sources = %#v", guidance["sources"])
	}
	skills, ok := payload["skills"].(map[string]any)
	if !ok {
		t.Fatalf("skills = %#v", payload["skills"])
	}
	items, ok := skills["items"].([]any)
	if !ok || len(items) != 2 ||
		items[0].(map[string]any)["name"] != "packaged-skill" || items[0].(map[string]any)["scope"] != "packaged" ||
		items[1].(map[string]any)["name"] != "project-skill" || items[1].(map[string]any)["scope"] != "project" {
		t.Fatalf("skill items = %#v", skills["items"])
	}
	if runtimeCalls != 0 {
		t.Fatalf("native agent guidance called runtime %d times", runtimeCalls)
	}
}
