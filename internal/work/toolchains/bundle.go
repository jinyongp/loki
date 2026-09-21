package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Provenance struct {
	Version     int                  `json:"version"`
	ManifestSHA string               `json:"manifest_sha256"`
	CatalogSHA  string               `json:"catalog_sha256,omitempty"`
	Artifacts   []ProvenanceArtifact `json:"artifacts"`
}

type ProvenanceArtifact struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type bundleArtifact struct {
	Name     string
	Filename string
	URL      string
	SHA256   string
}

func FetchBundle(ctx context.Context, client *http.Client, manifestRaw []byte, output string) error {
	return FetchBundleWithCatalog(ctx, client, manifestRaw, nil, output)
}

func FetchBundleWithCatalog(
	ctx context.Context,
	client *http.Client,
	manifestRaw, catalogRaw []byte,
	output string,
) error {
	manifest, err := LoadManifest(manifestRaw)
	if err != nil {
		return err
	}
	artifacts := make([]bundleArtifact, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		artifacts = append(artifacts, bundleArtifact{
			Name: artifact.Name, Filename: artifact.Filename, URL: artifact.URL, SHA256: artifact.SHA256,
		})
	}
	var catalog Catalog
	if len(catalogRaw) != 0 {
		catalog, err = LoadCatalog(catalogRaw)
		if err != nil {
			return err
		}
		artifacts, err = mergeBundleArtifacts(artifacts, catalogBundleArtifacts(catalog))
		if err != nil {
			return err
		}
	}
	if !filepath.IsAbs(output) {
		return errors.New("toolchain bundle output must be absolute")
	}
	if _, err = os.Lstat(output); !os.IsNotExist(err) {
		return errors.New("toolchain bundle output already exists")
	}
	parent := filepath.Dir(output)
	temp, err := os.MkdirTemp(parent, ".loki-toolchain-bundle-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	artifactDir := filepath.Join(temp, "artifacts")
	if err = os.Mkdir(artifactDir, 0755); err != nil {
		return err
	}

	provenance := Provenance{Version: 1, ManifestSHA: digest(manifestRaw)}
	if len(catalogRaw) != 0 {
		provenance.CatalogSHA = digest(catalogRaw)
	}
	checksums := []string{
		digest(manifestRaw) + "  manifest.json",
	}
	if len(catalogRaw) != 0 {
		checksums = append(checksums, digest(catalogRaw)+"  catalog.json")
	}
	for _, artifact := range artifacts {
		target := filepath.Join(artifactDir, artifact.Filename)
		if err = fetchOne(ctx, client, artifact.URL, target, artifact.SHA256); err != nil {
			return fmt.Errorf("fetch %s: %w", artifact.Name, err)
		}
		provenance.Artifacts = append(provenance.Artifacts, ProvenanceArtifact{
			Name: artifact.Name, URL: artifact.URL, SHA256: artifact.SHA256,
		})
		checksums = append(checksums, artifact.SHA256+"  artifacts/"+artifact.Filename)
	}
	if err = os.WriteFile(filepath.Join(temp, "manifest.json"), manifestRaw, 0644); err != nil {
		return err
	}
	if len(catalogRaw) != 0 {
		if err = os.WriteFile(filepath.Join(temp, "catalog.json"), catalogRaw, 0644); err != nil {
			return err
		}
	}
	encoded, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(temp, "provenance.json"), append(encoded, '\n'), 0644); err != nil {
		return err
	}
	sort.Strings(checksums)
	if err = os.WriteFile(filepath.Join(temp, "SHA256SUMS"), []byte(strings.Join(checksums, "\n")+"\n"), 0644); err != nil {
		return err
	}
	return os.Rename(temp, output)
}

func catalogBundleArtifacts(catalog Catalog) []bundleArtifact {
	result := make([]bundleArtifact, 0, len(catalog.Node)+len(catalog.Pnpm)+len(catalog.Python)+len(catalog.UV))
	for _, release := range catalog.Node {
		result = append(result, bundleArtifact{
			Name: "managed-node-" + release.Version, Filename: release.Filename(), URL: release.URL, SHA256: release.SHA256,
		})
	}
	for _, release := range catalog.Pnpm {
		result = append(result, bundleArtifact{
			Name: "managed-pnpm-" + release.Version, Filename: release.Filename(), URL: release.URL, SHA256: release.SHA256,
		})
	}
	for _, release := range catalog.Python {
		result = append(result, bundleArtifact{
			Name: "managed-python-" + release.Version, Filename: release.Filename(), URL: release.URL, SHA256: release.SHA256,
		})
	}
	for _, release := range catalog.UV {
		result = append(result, bundleArtifact{
			Name: "managed-uv-" + release.Version, Filename: release.Filename(), URL: release.URL, SHA256: release.SHA256,
		})
	}
	return result
}

func mergeBundleArtifacts(base, added []bundleArtifact) ([]bundleArtifact, error) {
	result := append([]bundleArtifact(nil), base...)
	byFilename := make(map[string]bundleArtifact, len(base)+len(added))
	for _, artifact := range base {
		if err := validateBundleArtifact(artifact); err != nil {
			return nil, err
		}
		if previous, exists := byFilename[artifact.Filename]; exists {
			if previous.URL != artifact.URL || previous.SHA256 != artifact.SHA256 {
				return nil, fmt.Errorf("toolchain artifact filename %q has conflicting identities", artifact.Filename)
			}
			continue
		}
		byFilename[artifact.Filename] = artifact
	}
	for _, artifact := range added {
		if err := validateBundleArtifact(artifact); err != nil {
			return nil, err
		}
		if previous, exists := byFilename[artifact.Filename]; exists {
			if previous.URL != artifact.URL || previous.SHA256 != artifact.SHA256 {
				return nil, fmt.Errorf("managed toolchain artifact filename %q conflicts with base manifest", artifact.Filename)
			}
			continue
		}
		byFilename[artifact.Filename] = artifact
		result = append(result, artifact)
	}
	return result, nil
}

func validateBundleArtifact(artifact bundleArtifact) error {
	if strings.TrimSpace(artifact.Name) == "" || filepath.Base(artifact.Filename) != artifact.Filename ||
		artifact.Filename == "." || artifact.Filename == "" || strings.ContainsRune(artifact.Filename, 0) ||
		!sha256Text.MatchString(artifact.SHA256) {
		return errors.New("toolchain bundle artifact identity is invalid")
	}
	return nil
}

func fetchOne(ctx context.Context, client *http.Client, source, target, want string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.Request.URL.Scheme != "https" {
		return errors.New("toolchain download redirected outside HTTPS")
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, 1<<30))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("checksum %s, want %s", got, want)
	}
	return os.Chmod(target, 0644)
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
