package mcptransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/config"
	"loki/internal/contract"
	"loki/internal/fault"
	"loki/internal/integrations/sharing/artifacts"
	"loki/internal/mcpserver"
	"loki/internal/work/workspace"
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
		{"artifact_publish", map[string]any{"action": "file", "path": "hello.txt", "request_id": "79000000-0000-4000-8000-000000000001"}},
		{"artifact_publish", map[string]any{"action": "bundle", "paths": []string{"hello.txt"}, "request_id": "79000000-0000-4000-8000-000000000002"}},
		{"share_image", map[string]any{"path": "image.png", "request_id": "78000000-0000-4000-8000-000000000001"}},
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
			if value["kind"] != tc.args["action"] {
				t.Fatalf("artifact kind = %#v, want %#v", value["kind"], tc.args["action"])
			}
			if tc.args["action"] == "bundle" {
				if value["mime_type"] != "application/zip" || value["file_count"] != float64(1) ||
					value["input_bytes"] != float64(5) || value["excluded_entries"] != float64(0) {
					t.Fatalf("bundle metadata = %#v", value)
				}
			}
		}
	}

	if err = os.Remove(filepath.Join(c.Root, "hello.txt")); err != nil {
		t.Fatal(err)
	}
	artifactRows := ArtifactList(store)["artifacts"].([]map[string]any)
	if len(artifactRows) != 3 {
		t.Fatal(artifactRows)
	}
	for _, test := range []struct {
		args     map[string]any
		filename string
		kind     string
	}{
		{args: map[string]any{"action": "file", "path": "hello.txt", "request_id": "79000000-0000-4000-8000-000000000001"}, filename: "hello.txt", kind: "file"},
		{args: map[string]any{"action": "bundle", "paths": []string{"hello.txt"}, "request_id": "79000000-0000-4000-8000-000000000002"}, filename: "loki-workspace.zip", kind: "bundle"},
	} {
		replayedArtifact, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: test.args})
		if err != nil || replayedArtifact.IsError {
			t.Fatalf("artifact replay %s: %v %+v", test.kind, err, replayedArtifact)
		}
		var value map[string]any
		encoded, _ := json.Marshal(replayedArtifact.StructuredContent)
		if err = json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		var original map[string]any
		for _, row := range artifactRows {
			if row["filename"] == test.filename {
				original = row
				break
			}
		}
		if original == nil || value["share_id"] != original["share_id"] || value["url"] != original["url"] || value["kind"] != test.kind {
			t.Fatalf("artifact replay identity %s = replay=%#v original=%#v", test.kind, value, original)
		}
		if test.kind == "bundle" && (value["file_count"] != float64(1) || value["input_bytes"] != float64(5)) {
			t.Fatalf("bundle replay metadata = %#v", value)
		}
	}
	artifactConflict, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: map[string]any{
		"action": "bundle", "paths": []string{"hello.txt"}, "filename": "changed.zip",
		"request_id": "79000000-0000-4000-8000-000000000002",
	}})
	if err != nil || !artifactConflict.IsError || len(ArtifactList(store)["artifacts"].([]map[string]any)) != 3 {
		t.Fatalf("changed artifact replay did not conflict: result=%#v err=%v", artifactConflict, err)
	}
	if detail := artifactConflict.Meta["loki/error"].(map[string]any); detail["code"] != "conflict" {
		t.Fatalf("changed artifact replay error = %#v", detail)
	}

	if err = os.Remove(filepath.Join(c.Root, "image.png")); err != nil {
		t.Fatal(err)
	}
	replayed, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "share_image", Arguments: map[string]any{
		"path": "image.png", "request_id": "78000000-0000-4000-8000-000000000001",
	}})
	if err != nil || replayed.IsError {
		t.Fatalf("share image replay: %v %+v", err, replayed)
	}
	var replayValue map[string]any
	replayEncoded, _ := json.Marshal(replayed.StructuredContent)
	if err = json.Unmarshal(replayEncoded, &replayValue); err != nil {
		t.Fatal(err)
	}
	rows := ArtifactList(store)["artifacts"].([]map[string]any)
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	shareRows := []map[string]any{}
	for _, row := range rows {
		if row["filename"] == "image.png" {
			shareRows = append(shareRows, row)
		}
	}
	if len(shareRows) != 1 || replayValue["share_id"] != shareRows[0]["share_id"] || replayValue["url"] != shareRows[0]["url"] {
		t.Fatalf("share replay identity = replay=%#v rows=%#v", replayValue, shareRows)
	}
	beforeConflict := len(rows)
	conflict, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "share_image", Arguments: map[string]any{
		"path": "image.png", "request_id": "78000000-0000-4000-8000-000000000001", "ttl_seconds": 1200,
	}})
	if err != nil || !conflict.IsError || len(ArtifactList(store)["artifacts"].([]map[string]any)) != beforeConflict {
		t.Fatalf("changed share replay did not conflict: result=%#v err=%v", conflict, err)
	}
	if detail := conflict.Meta["loki/error"].(map[string]any); detail["code"] != "conflict" {
		t.Fatalf("changed share replay error = %#v", detail)
	}
	id := rows[0]["share_id"].(string)
	firstRevoke, err := ArtifactRevoke(store, id)
	if err != nil || firstRevoke["kind"] != "artifact" || firstRevoke["share_id"] != id || firstRevoke["revoked"] != true {
		t.Fatalf("first revoke = %#v, %v", firstRevoke, err)
	}
	w := httptest.NewRecorder()
	store.ServeHTTP(w, httptest.NewRequest("GET", rows[0]["url"].(string), nil))
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	secondRevoke, err := ArtifactRevoke(store, id)
	if err != nil || secondRevoke["kind"] != "artifact" || secondRevoke["share_id"] != id || secondRevoke["revoked"] != true {
		t.Fatalf("second revoke = %#v, %v", secondRevoke, err)
	}
	r, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "artifact_publish", Arguments: map[string]any{
		"action": "file", "path": "hello.txt", "ttl_seconds": 1,
		"request_id": "79000000-0000-4000-8000-000000000003",
	}})
	if err != nil || !r.IsError {
		t.Fatalf("TTL: %v %+v", err, r)
	}
}

func TestArtifactCapacityErrorsAreRetryableQuota(t *testing.T) {
	for name, err := range map[string]error{
		"artifact": artifacts.ErrCapacityFull,
		"replay":   artifacts.ErrReplayCapacityFull,
	} {
		t.Run(name, func(t *testing.T) {
			for _, mapped := range []error{artifactPublishReplayError(err), shareImageReplayError(err)} {
				detail := fault.Describe(mapped)
				if detail.Code != fault.CodeQuotaExceeded || !detail.Retryable ||
					!strings.Contains(detail.NextAction, "retry") {
					t.Fatalf("artifact capacity detail = %#v", detail)
				}
			}
		})
	}
}
