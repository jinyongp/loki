package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/config"
	"loki/internal/work/workspace"
)

type browserUploadFixture struct {
	seenOperation string
	seenArgs      map[string]any
	stage         *stagedBrowserUpload
}

func (f *browserUploadFixture) Call(_ context.Context, operation string, args map[string]any) (map[string]any, error) {
	f.seenOperation = operation
	f.seenArgs = args
	return map[string]any{
		"uploaded":           true,
		"index":              args["index"],
		"file_count":         len(args["staged_files"].([]any)),
		"total_bytes":        f.stage.TotalBytes,
		"browser_generation": 8,
	}, nil
}

func (f *browserUploadFixture) StageBrowserUpload(_ context.Context, _ *workspace.Files, paths []string) (*stagedBrowserUpload, error) {
	if len(paths) != 1 || paths[0] != "fixtures/input.txt" {
		return nil, os.ErrInvalid
	}
	return f.stage, nil
}

func TestBrowserUploadHandlerStagesPathsBeforeRPC(t *testing.T) {
	token := strings.Repeat("a", 32)
	client := &browserUploadFixture{
		stage: &stagedBrowserUpload{
			Tokens: []string{token},
			Files: []map[string]any{{
				"path": "fixtures/input.txt", "name": "input.txt", "bytes": int64(4),
			}},
			TotalBytes: 4,
		},
	}
	handler := BrowserHandlers(client, nil, nil)["browser_interact"]
	result, err := handler(t.Context(), map[string]any{
		"action": "upload", "index": 3,
		"paths":                       []any{"fixtures/input.txt"},
		"expected_browser_generation": 7,
		"expected_state_generation":   2,
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("browser upload result = %#v err=%v", result, err)
	}
	if client.seenOperation != "upload" {
		t.Fatalf("upload operation = %q", client.seenOperation)
	}
	if _, exists := client.seenArgs["paths"]; exists {
		t.Fatalf("workspace paths leaked to browser RPC: %#v", client.seenArgs)
	}
	refs, ok := client.seenArgs["staged_files"].([]any)
	if !ok || len(refs) != 1 {
		t.Fatalf("staged refs = %#v", client.seenArgs["staged_files"])
	}
	ref := refs[0].(map[string]any)
	if ref["token"] != token || ref["name"] != "input.txt" {
		t.Fatalf("staged ref = %#v", ref)
	}
	output := result.StructuredContent.(map[string]any)
	if _, exists := output["file_count"]; exists {
		t.Fatalf("internal file_count leaked: %#v", output)
	}
	files := output["files"].([]map[string]any)
	if len(files) != 1 || files[0]["path"] != "fixtures/input.txt" || files[0]["name"] != "input.txt" {
		t.Fatalf("public upload files = %#v", files)
	}
}

func TestBrowserRPCStagesPolicyCheckedWorkspaceFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "one.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("one.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	configuration, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	configuration.Root = root
	configuration.BrowserMaxUploadFiles = 2
	configuration.BrowserMaxUploadBytes = 16
	files, err := workspace.New(configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()

	socketDir := t.TempDir()
	socket := filepath.Join(socketDir, "browser.sock")
	if err = os.WriteFile(socket, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(socketDir, "uploads")
	if err = os.Mkdir(inbox, 0o770); err != nil {
		t.Fatal(err)
	}
	rpc := BrowserRPC{}
	rpc.Client.Socket = socket

	stage, err := rpc.StageBrowserUpload(t.Context(), files, []string{"one.txt", "nested/one.txt"})
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Cleanup()
	if len(stage.Tokens) != 2 || len(stage.Files) != 2 || stage.TotalBytes != 6 {
		t.Fatalf("staged upload = %#v", stage)
	}
	if stage.Files[0]["name"] != "one.txt" || stage.Files[1]["name"] != "one.txt" {
		t.Fatalf("staged filenames = %#v", stage.Files)
	}
	for index, token := range stage.Tokens {
		if !uploadTokenPatternForTest(token) {
			t.Fatalf("invalid upload token %q", token)
		}
		data, err := os.ReadFile(filepath.Join(inbox, token))
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"one", "two"}[index]
		if string(data) != want {
			t.Fatalf("staged token %d = %q, want %q", index, data, want)
		}
		info, err := os.Stat(filepath.Join(inbox, token))
		if err != nil || info.Mode().Perm() != 0o640 {
			t.Fatalf("staged token mode = %#o err=%v", info.Mode().Perm(), err)
		}
	}
	stage.Cleanup()
	for _, token := range stage.Tokens {
		if _, err := os.Stat(filepath.Join(inbox, token)); !os.IsNotExist(err) {
			t.Fatalf("staged token survived cleanup: %s %v", token, err)
		}
	}

	for _, path := range []string{".env", "link.txt"} {
		if _, err := rpc.StageBrowserUpload(t.Context(), files, []string{path}); err == nil {
			t.Fatalf("unsafe upload accepted: %s", path)
		}
	}
}

func uploadTokenPatternForTest(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
