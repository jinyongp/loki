package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"loki/internal/management"
	"loki/internal/tools"
)

// Explicit catalogs remain available for reviewed offline distributions. The
// ordinary path acquires its catalog and checksum from the same stable release.
func acquireCatalog(ctx context.Context, store management.Store, path, version string, diagnostics io.Writer, client *http.Client) (tools.Catalog, error) {
	state, err := store.Load()
	if err != nil {
		return tools.Catalog{}, err
	}
	return acquireCatalogForMode(ctx, state.Config.Mode, path, version, diagnostics, client)
}

func acquireCatalogForMode(ctx context.Context, mode tools.Mode, path, version string, diagnostics io.Writer, client *http.Client) (tools.Catalog, error) {
	if path != "" {
		if version != "" {
			return tools.Catalog{}, fmt.Errorf("use --version or --catalog, not both")
		}
		file, err := os.Open(path)
		if err != nil {
			return tools.Catalog{}, err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, tools.MaxManifestBytes+1))
		if err != nil {
			return tools.Catalog{}, err
		}
		return tools.ParseCatalog(data)
	}
	if version == "" {
		version = management.ManagerRelease
	}
	if !stableVersionPattern.MatchString(version) {
		return tools.Catalog{}, fmt.Errorf("tool release must be a stable MAJOR.MINOR.PATCH version")
	}
	target := management.LocalTarget(mode)
	asset := fmt.Sprintf("loki-catalog-%s-%s-%s.json", target.OS, target.Arch, target.Mode)
	base := "https://github.com/jinyongp/loki/releases/download/v" + version + "/"
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	fmt.Fprintf(diagnostics, "Checking trusted %s/%s %s tools for Loki %s...\n", target.OS, target.Arch, target.Mode, version)
	sums, err := upgradeDownload(ctx, client, base+"SHA256SUMS", 1<<20)
	if err != nil {
		return tools.Catalog{}, fmt.Errorf("acquiring release checksums: %w", err)
	}
	data, err := upgradeDownload(ctx, client, base+asset, tools.MaxManifestBytes)
	if err != nil {
		return tools.Catalog{}, fmt.Errorf("acquiring tools for %s/%s (%s): %w", target.OS, target.Arch, target.Mode, err)
	}
	if err := verifyReleaseAsset(data, sums, asset); err != nil {
		return tools.Catalog{}, err
	}
	catalog, err := tools.ParseCatalog(data)
	if err != nil {
		return tools.Catalog{}, err
	}
	if catalog.Release != version {
		return tools.Catalog{}, fmt.Errorf("catalog version differs from its verified release")
	}
	return catalog, nil
}

func verifyReleaseAsset(data, checksums []byte, asset string) error {
	var expected string
	for _, line := range strings.Split(string(checksums), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 && strings.TrimPrefix(parts[1], "*") == asset {
			if expected != "" {
				return fmt.Errorf("duplicate release asset checksum")
			}
			expected = parts[0]
		}
	}
	digest, err := hex.DecodeString(expected)
	if err != nil || len(digest) != sha256.Size {
		return fmt.Errorf("release is missing a valid %s checksum", asset)
	}
	actual := sha256.Sum256(data)
	if !bytes.Equal(digest, actual[:]) {
		return fmt.Errorf("%s SHA-256 verification failed", asset)
	}
	return nil
}
