package workspace

import (
	"context"
	"loki/internal/config"
	"os"
	"path/filepath"
	"testing"
)

func TestStandaloneFilesNeedNoRepositoryAdapter(t *testing.T) {
	configuration, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	configuration.Root = t.TempDir()
	files, err := New(configuration)
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	if err := os.WriteFile(filepath.Join(configuration.Root, "file.txt"), []byte("original\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := files.Read("file.txt", 0, 10); err != nil {
		t.Fatal("ordinary reads acquired Git", err)
	}
	if _, err := files.RemoveTracked(context.Background(), "file.txt", Digest([]byte("original\n"))); err == nil {
		t.Fatal("repository-specific removal ignored missing adapter")
	}
	if data, err := os.ReadFile(filepath.Join(configuration.Root, "file.txt")); err != nil || string(data) != "original\n" {
		t.Fatal("unavailable repository operation changed file", err)
	}
}
