package toolchain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const ManifestVersion = 1

var (
	packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*$`)
	sha256Text  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Manifest struct {
	Version     int          `json:"version"`
	Platform    Platform     `json:"platform"`
	AptPackages []AptPackage `json:"apt_packages"`
	Artifacts   []Artifact   `json:"artifacts"`
}

type Platform struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Arch    string `json:"arch"`
}

type AptPackage struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Artifact struct {
	Name            string            `json:"name"`
	Version         string            `json:"version"`
	Filename        string            `json:"filename"`
	URL             string            `json:"url"`
	SHA256          string            `json:"sha256"`
	Format          string            `json:"format"`
	InstallPath     string            `json:"install_path"`
	StripComponents int               `json:"strip_components"`
	Links           map[string]string `json:"links"`
}

func LoadManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode toolchain manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("toolchain manifest contains trailing data")
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.Version != ManifestVersion {
		return fmt.Errorf("unsupported toolchain manifest version %d", m.Version)
	}
	if m.Platform != (Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}) {
		return fmt.Errorf("unsupported toolchain platform %#v", m.Platform)
	}
	if len(m.AptPackages) == 0 || len(m.Artifacts) == 0 {
		return errors.New("toolchain manifest is incomplete")
	}
	previous := ""
	for _, item := range m.AptPackages {
		if !packageName.MatchString(item.Name) || item.Name <= previous || item.Version == "" || strings.ContainsAny(item.Version, "\r\n\x00") {
			return fmt.Errorf("invalid or unsorted apt package %q", item.Name)
		}
		previous = item.Name
	}
	previous = ""
	for _, item := range m.Artifacts {
		parsed, err := url.Parse(item.URL)
		if item.Name <= previous || !packageName.MatchString(item.Name) || item.Version == "" || filepath.Base(item.Filename) != item.Filename || !sha256Text.MatchString(item.SHA256) || err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("invalid or unsorted toolchain artifact %q", item.Name)
		}
		toolPath := strings.HasPrefix(item.InstallPath, "/opt/loki/toolchain/")
		licensePath := strings.HasPrefix(item.InstallPath, "/usr/share/doc/loki/licenses/")
		if !slices.Contains([]string{"file", "tar.gz", "tar.xz", "zip"}, item.Format) || item.StripComponents < 0 || item.StripComponents > 1 || !filepath.IsAbs(item.InstallPath) || filepath.Clean(item.InstallPath) != item.InstallPath || (!toolPath && !licensePath) {
			return fmt.Errorf("invalid install contract for toolchain artifact %q", item.Name)
		}
		if item.Format == "file" && item.StripComponents != 0 {
			return fmt.Errorf("file artifact %q cannot strip components", item.Name)
		}
		if toolPath && len(item.Links) == 0 {
			return fmt.Errorf("toolchain artifact %q has no executable links", item.Name)
		}
		if licensePath && (item.Format != "file" || len(item.Links) != 0) {
			return fmt.Errorf("invalid license artifact %q", item.Name)
		}
		for name, target := range item.Links {
			if !packageName.MatchString(name) || target == "" || filepath.IsAbs(target) || filepath.Clean(target) != target || target == ".." || strings.HasPrefix(target, "../") {
				return fmt.Errorf("invalid executable link %q for toolchain artifact %q", name, item.Name)
			}
		}
		previous = item.Name
	}
	return nil
}
