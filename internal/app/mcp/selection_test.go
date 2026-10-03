package mcpapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/portguard"
)

func TestSelectedWorkspaceNeedsNoRuntimeGitOrCredentials(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewMCP(c, MCPOptions{Tools: []string{"workspace"}, Token: strings.Repeat("t", 43), Policy: policyGenerationFixture(t), Ports: ports})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.Claims != nil || app.Artifacts != nil || app.Previews != nil || app.browserSessions != nil {
		t.Fatal("workspace initialized another tool")
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := app.Server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "selection-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	foundRead := false
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "workspace_read" {
			foundRead = true
		}
		if strings.HasPrefix(tool.Name, "git_") || strings.HasPrefix(tool.Name, "secret_") || strings.HasPrefix(tool.Name, "github_") || strings.HasPrefix(tool.Name, "job_") || tool.Name == "remove_tracked_file" {
			t.Fatalf("unselected binding: %s", tool.Name)
		}
	}
	if !foundRead {
		t.Fatal("workspace read is missing")
	}
}

func TestEmptyCompositionDoesNotOpenWorkspace(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = filepath.Join(t.TempDir(), "absent-workspace")
	c.AuditLog = filepath.Join(t.TempDir(), "audit.jsonl")
	ports, err := portguard.NewPolicy(c.Port, 18766, 18767)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewMCP(c, MCPOptions{Tools: []string{}, Token: strings.Repeat("t", 43), Policy: policyGenerationFixture(t), Ports: ports})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if app.files != nil {
		t.Fatal("empty composition opened workspace")
	}
	if _, err := os.Stat(c.Root); !os.IsNotExist(err) {
		t.Fatalf("empty composition created workspace: %v", err)
	}
}
