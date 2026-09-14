package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/artifacts"
	"loki/internal/config"
	"loki/internal/contract"
	"loki/internal/mcpserver"
	"loki/internal/workspace"
)

func TestArtifactMCP(t *testing.T) {
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = t.TempDir()
	f, err := workspace.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for name, data := range map[string][]byte{"hello.txt": []byte("hello"), "image.png": []byte("\x89PNG\r\n\x1a\nimage")} {
		if err := os.WriteFile(filepath.Join(c.Root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := artifacts.New(artifacts.Options{BaseURL: "https://example.test/artifacts", AllowedHosts: []string{"example.test"}})
	handlers := ArtifactHandlers(f, store)
	defs, _ := contract.CurrentDefinitions()
	for _, d := range defs {
		if handlers[d.Name] == nil {
			handlers[d.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("outside artifact test")
			}
		}
	}
	s, err := mcpserver.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ss, err := s.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "artifacts-test", Version: "1"}, nil).Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"artifact_publish", map[string]any{"action": "file", "path": "hello.txt"}},
		{"artifact_publish", map[string]any{"action": "bundle", "paths": []string{"hello.txt"}}},
		{"share_image", map[string]any{"path": "image.png"}},
	} {
		r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err != nil || r.IsError {
			t.Fatalf("%s: %v %+v", tc.name, err, r)
		}
		encoded, _ := json.Marshal(r.StructuredContent)
		var value map[string]any
		if err = json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		url := value["url"].(string)
		w := httptest.NewRecorder()
		store.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		if w.Code != 200 || float64(w.Body.Len()) != value["bytes"] {
			t.Fatalf("%d %s", w.Code, encoded)
		}
		if tc.name == "artifact_publish" {
			if len(r.Content) != 2 {
				t.Fatal(r.Content)
			}
			if _, ok := r.Content[1].(*mcp.ResourceLink); !ok {
				t.Fatalf("%T", r.Content[1])
			}
		}
	}
	rows := ArtifactList(store)["artifacts"].([]map[string]any)
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	id := rows[0]["share_id"].(string)
	if _, err = ArtifactRevoke(store, id); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest("GET", rows[0]["url"].(string), nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	if _, err = ArtifactRevoke(store, id); err == nil {
		t.Fatal("double revoke")
	}
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: map[string]any{"action": "file", "path": "hello.txt", "ttl_seconds": 1}})
	if err != nil || !r.IsError {
		t.Fatalf("TTL: %v %+v", err, r)
	}
}
