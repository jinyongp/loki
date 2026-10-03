package toolproxy

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestBrowserFileTransferAndCachedResourceGates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	var enabled atomic.Bool
	enabled.Store(true)
	server := mcp.NewServer(&mcp.Implementation{Name: "files-fixture", Version: "0.2.0"}, nil)
	closeFiles, err := registerFiles(ctx, server, &bindingOwners{}, []Options{{Owner: "browser/playwright", Results: dir, AuthorizeResource: func() error {
		if !enabled.Load() {
			return errors.New("browser disabled")
		}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFiles()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "client-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "loki_browser_files", Arguments: map[string]any{"engine": "browser/playwright", "action": "stage", "name": "fixture.txt", "data": base64.StdEncoding.EncodeToString([]byte("remote upload"))}})
	if err != nil || result.IsError {
		t.Fatalf("stage failed: %+v %v", result, err)
	}
	var entry fileEntry
	if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &entry); err != nil {
		t.Fatal(err)
	}
	resource, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: entry.URI})
	if err != nil {
		t.Fatal(err)
	}
	if string(resource.Contents[0].Blob) != "remote upload" || entry.HostPath != filepath.Join(dir, "inputs", "fixture.txt") {
		t.Fatal("upload was not readable as an owned resource")
	}
	enabled.Store(false)
	if _, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: entry.URI}); err == nil {
		t.Fatal("cached resource bypassed disabled browser")
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "loki_browser_files", Arguments: map[string]any{"engine": "browser/playwright", "action": "read", "name": "inputs/fixture.txt"}})
	if err == nil && !result.IsError {
		t.Fatal("cached helper bypassed disabled browser")
	}
}

func TestBrowserFilesRejectTraversalAndExternalSymlinks(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f := &fileSession{directory: dir, root: root, prefix: "loki://browser/files/fixture/", authorize: func() error { return nil }}
	for _, name := range []string{"../outside", "/outside", "C:/outside", "nested/file", "a\\file"} {
		if _, err := f.stage(name, "YQ=="); err == nil {
			t.Fatalf("accepted upload path %q", name)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("user data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skip("symbolic links unavailable on this native runner:", err)
	}
	if _, _, err := f.read("escape"); err == nil {
		t.Fatal("read an external user file")
	}
	entries, err := f.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("listed an external symlink")
	}
}
