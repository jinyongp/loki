// Package assets owns the canonical host-side deployment assets materialized by
// the Loki host manager.
package assets

import (
	"embed"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"loki/internal/platform/safeio"
)

//go:generate go run ../../../tools/renderhostassets
//go:embed files/compose.yaml files/github.compose.toml files/ingress.compose.toml
var files embed.FS

func Compose() []byte {
	raw, err := files.ReadFile("files/compose.yaml")
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), raw...)
}

func GitHubConfig() []byte {
	raw, err := files.ReadFile("files/github.compose.toml")
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), raw...)
}

func IngressConfig() []byte {
	raw, err := files.ReadFile("files/ingress.compose.toml")
	if err != nil {
		panic(err)
	}
	return append([]byte(nil), raw...)
}

type Materialized struct {
	Root          string
	ComposePath   string
	GitHubConfig  string
	IngressConfig string
}

func Materialize(root string) (Materialized, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
		return Materialized{}, errors.New("host asset root must be a clean absolute non-root path")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return Materialized{}, err
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return Materialized{}, errors.New("host asset root must be a private real directory")
	}
	composePath := filepath.Join(root, "compose.yaml")
	githubPath := filepath.Join(root, "github.compose.toml")
	ingressPath := filepath.Join(root, "ingress.compose.toml")
	if err = safeio.PublishPrivate(composePath, Compose(), true); err != nil {
		return Materialized{}, err
	}
	if err = safeio.PublishPrivate(githubPath, GitHubConfig(), true); err != nil {
		return Materialized{}, err
	}
	if err = safeio.PublishPrivate(ingressPath, IngressConfig(), true); err != nil {
		return Materialized{}, err
	}
	// Compose mounts this non-secret operator configuration directly into
	// capability-dropped service containers. Keep the parent directory private
	// on the host while making the bind-mounted file readable in-container.
	if err = os.Chmod(githubPath, 0644); err != nil {
		return Materialized{}, err
	}
	if err = os.Chmod(ingressPath, 0644); err != nil {
		return Materialized{}, err
	}
	return Materialized{Root: root, ComposePath: composePath, GitHubConfig: githubPath, IngressConfig: ingressPath}, nil
}

func WriteDeveloperViews(repositoryRoot string) error {
	if !filepath.IsAbs(repositoryRoot) || filepath.Clean(repositoryRoot) != repositoryRoot {
		return errors.New("repository root must be a clean absolute path")
	}
	if err := os.WriteFile(filepath.Join(repositoryRoot, "compose.yaml"), Compose(), 0644); err != nil {
		return err
	}
	configRoot := filepath.Join(repositoryRoot, "config")
	if err := os.MkdirAll(configRoot, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(configRoot, "github.compose.toml"), GitHubConfig(), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configRoot, "ingress.compose.toml"), IngressConfig(), 0644)
}
