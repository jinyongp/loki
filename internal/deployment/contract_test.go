package deployment

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func repositoryContract(t *testing.T) Contract {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "native", "deployment-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func TestRepositoryContractDefinesPortableTopology(t *testing.T) {
	c := repositoryContract(t)
	if !c.Services["runtime"].VaultAccess || c.Services["mcp"].VaultAccess {
		t.Fatal("vault ownership is not isolated")
	}
	if !c.Networks["private"].Internal || c.Networks["outbound"].Internal {
		t.Fatal("network boundary is invalid")
	}
	if !c.Images["loki-browser"].Optional || c.Features["browser"].Default || c.Features["github"].Default {
		t.Fatal("optional features are enabled")
	}
	if got := c.Features["browser"].Services; !slices.Equal(got, []string{"browser", "browser-proxy"}) {
		t.Fatalf("browser feature services = %v", got)
	}
	if !slices.Equal(c.Services["browser"].Networks, []string{"private"}) ||
		!slices.Equal(c.Services["browser-proxy"].Networks, []string{"outbound", "private"}) {
		t.Fatal("browser egress boundary is invalid")
	}
	if got := c.Features["docker"].Services; !slices.Equal(got, []string{"launcher"}) {
		t.Fatalf("Docker feature services = %v", got)
	}
	if !c.Services["launcher"].Required || c.Services["launcher"].VaultAccess ||
		c.Services["launcher"].WorkspaceAccess != "none" ||
		!c.Services["executor"].Required || c.Services["executor"].VaultAccess ||
		c.Services["executor"].WorkspaceAccess != "none" {
		t.Fatal("Job control role authority is invalid")
	}
	if socket := c.Sockets["launcher"]; socket.Owner != "launcher" || !slices.Equal(socket.Clients, []string{"executor"}) {
		t.Fatalf("launcher socket = %#v", socket)
	}
	if socket := c.Sockets["executor"]; socket.Owner != "executor" || !slices.Equal(socket.Clients, []string{"mcp"}) {
		t.Fatalf("executor socket = %#v", socket)
	}
	if !c.Volumes["workspace"].External || c.Volumes["sockets"].Persistent {
		t.Fatal("volume lifecycle is invalid")
	}
}

func TestContractRejectsPrivilegeAndTopologyDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Contract)
		want   string
	}{
		{"MCP vault", func(c *Contract) { s := c.Services["mcp"]; s.VaultAccess = true; c.Services["mcp"] = s }, "vault access"},
		{"runtime outbound", func(c *Contract) {
			s := c.Services["runtime"]
			s.Networks = []string{"outbound", "private"}
			c.Services["runtime"] = s
		}, "outbound access"},
		{"writable root", func(c *Contract) { s := c.Services["runtime"]; s.ReadOnlyRoot = false; c.Services["runtime"] = s }, "resource policy"},
		{"browser default", func(c *Contract) { f := c.Features["browser"]; f.Default = true; c.Features["browser"] = f }, "optional feature"},
		{"socket client", func(c *Contract) {
			s := c.Sockets["runtime"]
			s.Clients = []string{"missing"}
			c.Sockets["runtime"] = s
		}, "invalid client"},
		{"volume collision", func(c *Contract) {
			v := c.Volumes["cache"]
			v.Path = c.Volumes["runtime-state"].Path
			c.Volumes["cache"] = v
		}, "share a path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := repositoryContract(t)
			test.mutate(&c)
			if err := c.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadRejectsUnknownAndTrailingData(t *testing.T) {
	for _, raw := range []string{`{"version":1,"unknown":true}`, `{"version":1} {}`} {
		if _, err := Load([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
