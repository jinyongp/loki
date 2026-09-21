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
	Artifacts   []ProvenanceArtifact `json:"artifacts"`
}

type ProvenanceArtifact struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

func FetchBundle(ctx context.Context, client *http.Client, raw []byte, output string) error {
	manifest, err := LoadManifest(raw)
	if err != nil {
		return err
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
	artifacts := filepath.Join(temp, "artifacts")
	if err = os.Mkdir(artifacts, 0755); err != nil {
		return err
	}
	provenance := Provenance{Version: 1, ManifestSHA: digest(raw)}
	checksums := []string{}
	for _, artifact := range manifest.Artifacts {
		target := filepath.Join(artifacts, artifact.Filename)
		if err = fetchOne(ctx, client, artifact.URL, target, artifact.SHA256); err != nil {
			return fmt.Errorf("fetch %s: %w", artifact.Name, err)
		}
		provenance.Artifacts = append(provenance.Artifacts, ProvenanceArtifact{Name: artifact.Name, URL: artifact.URL, SHA256: artifact.SHA256})
		checksums = append(checksums, artifact.SHA256+"  artifacts/"+artifact.Filename)
	}
	if err = os.WriteFile(filepath.Join(temp, "manifest.json"), raw, 0644); err != nil {
		return err
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
