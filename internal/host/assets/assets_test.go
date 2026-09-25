package assets

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCanonicalAssetsMatchDeveloperViews(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	compose, err := os.ReadFile(filepath.Join(root, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(compose, Compose()) {
		t.Fatal("root compose.yaml drifted from canonical embedded host asset")
	}
	github, err := os.ReadFile(filepath.Join(root, "config", "github.compose.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(github, GitHubConfig()) {
		t.Fatal("config/github.compose.toml drifted from canonical embedded host asset")
	}
}

func TestMaterializeUsesPrivateRootAndContainerReadablePublicConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assets")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := Materialize(root)
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string][]byte{
		result.ComposePath:  Compose(),
		result.GitHubConfig: GitHubConfig(),
	} {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(raw, want) {
			t.Fatalf("materialized asset %s differs", path)
		}
	}
	for path, wantMode := range map[string]os.FileMode{
		result.ComposePath:  0600,
		result.GitHubConfig: 0644,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != wantMode {
			t.Fatalf("materialized asset mode %s = %v, %v; want %04o", path, info, statErr, wantMode)
		}
	}
}
