package toolchain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const CatalogVersion = 1

type Catalog struct {
	Version int             `json:"version"`
	Node    []NodeRelease   `json:"node,omitempty"`
	Pnpm    []PnpmRelease   `json:"pnpm,omitempty"`
	Python  []PythonRelease `json:"python,omitempty"`
	UV      []UVRelease     `json:"uv,omitempty"`
	Rust    []RustRelease   `json:"rust,omitempty"`
	Go      []GoRelease     `json:"go,omitempty"`
}

func LoadCatalog(raw []byte) (Catalog, error) {
	var catalog Catalog
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode toolchain catalog: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Catalog{}, errors.New("toolchain catalog contains trailing data")
	}
	if err := catalog.Validate(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func (c Catalog) Validate() error {
	if c.Version != CatalogVersion {
		return fmt.Errorf("unsupported toolchain catalog version %d", c.Version)
	}
	if len(c.Node)+len(c.Pnpm)+len(c.Python)+len(c.UV)+len(c.Rust)+len(c.Go) == 0 {
		return errors.New("toolchain catalog has no releases")
	}
	if err := validateNodeCatalog(c.Node); err != nil {
		return err
	}
	if err := validatePnpmCatalog(c.Pnpm); err != nil {
		return err
	}
	if err := validatePythonCatalog(c.Python); err != nil {
		return err
	}
	if err := validateUVCatalog(c.UV); err != nil {
		return err
	}
	if err := validateRustCatalog(c.Rust); err != nil {
		return err
	}
	if err := validateGoCatalog(c.Go); err != nil {
		return err
	}
	return nil
}

func validateNodeCatalog(releases []NodeRelease) error {
	scheme := NodeVersionScheme{}
	for index, release := range releases {
		if err := release.Validate(); err != nil {
			return err
		}
		if index > 0 {
			comparison, err := scheme.Compare(releases[index-1].Version, release.Version)
			if err != nil {
				return err
			}
			if comparison >= 0 {
				return errors.New("Node.js catalog releases must be unique and sorted")
			}
		}
	}
	return nil
}

func validatePnpmCatalog(releases []PnpmRelease) error {
	scheme := PnpmVersionScheme{}
	for index, release := range releases {
		if err := release.Validate(); err != nil {
			return err
		}
		if index > 0 {
			comparison, err := scheme.Compare(releases[index-1].Version, release.Version)
			if err != nil {
				return err
			}
			if comparison >= 0 {
				return errors.New("pnpm catalog releases must be unique and sorted")
			}
		}
	}
	return nil
}

func validatePythonCatalog(releases []PythonRelease) error {
	scheme := PythonVersionScheme{}
	for index, release := range releases {
		if err := release.Validate(); err != nil {
			return err
		}
		if index > 0 {
			comparison, err := scheme.Compare(releases[index-1].Version, release.Version)
			if err != nil {
				return err
			}
			if comparison >= 0 {
				return errors.New("Python catalog releases must be unique and sorted")
			}
		}
	}
	return nil
}

func validateUVCatalog(releases []UVRelease) error {
	scheme := UVVersionScheme{}
	for index, release := range releases {
		if err := release.Validate(); err != nil {
			return err
		}
		if index > 0 {
			comparison, err := scheme.Compare(releases[index-1].Version, release.Version)
			if err != nil {
				return err
			}
			if comparison >= 0 {
				return errors.New("uv catalog releases must be unique and sorted")
			}
		}
	}
	return nil
}

func validateRustCatalog(releases []RustRelease) error {
	for index, release := range releases {
		if err := release.Validate(); err != nil {
			return err
		}
		if index > 0 && compareRustRelease(releases[index-1], release) >= 0 {
			return errors.New("Rust catalog releases must be unique and sorted")
		}
	}
	return nil
}

func validateGoCatalog(releases []GoRelease) error {
	scheme := GoVersionScheme{}
	for index, release := range releases {
		if err := release.Validate(); err != nil {
			return err
		}
		if index > 0 {
			comparison, err := scheme.Compare(releases[index-1].Version, release.Version)
			if err != nil {
				return err
			}
			if comparison >= 0 {
				return errors.New("Go catalog releases must be unique and sorted")
			}
		}
	}
	return nil
}

func catalogGenerationIDs(catalog Catalog) []string {
	result := make([]string, 0, len(catalog.Node)+len(catalog.Pnpm)+len(catalog.Python)+len(catalog.UV)+len(catalog.Rust)+len(catalog.Go))
	for _, release := range catalog.Node {
		result = append(result, release.GenerationID())
	}
	for _, release := range catalog.Pnpm {
		result = append(result, release.GenerationID())
	}
	for _, release := range catalog.Python {
		result = append(result, release.GenerationID())
	}
	for _, release := range catalog.UV {
		result = append(result, release.GenerationID())
	}
	for _, release := range catalog.Rust {
		result = append(result, release.GenerationID())
	}
	for _, release := range catalog.Go {
		result = append(result, release.GenerationID())
	}
	return result
}

func ProvisionCatalog(ctx context.Context, catalog Catalog, bundle string, store GenerationStore) error {
	if err := catalog.Validate(); err != nil {
		return err
	}
	store.Protected = catalogGenerationIDs(catalog)
	if !filepath.IsAbs(bundle) || filepath.Clean(bundle) != bundle {
		return errors.New("toolchain catalog bundle must be a clean absolute path")
	}
	artifacts := filepath.Join(bundle, "artifacts")
	info, err := os.Lstat(artifacts)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("toolchain catalog artifact directory is unavailable")
	}

	nodeProvider := NodeProvider{Store: store}
	for _, release := range catalog.Node {
		plan, resolveErr := nodeProvider.Resolve(release.Version, catalog.Node, false)
		if resolveErr != nil {
			return resolveErr
		}
		if plan.Resolution.Acquire {
			source := filepath.Join(artifacts, release.Filename())
			if _, err = nodeProvider.Provision(ctx, plan, source); err != nil {
				return err
			}
		}
	}

	pnpmProvider := PnpmProvider{Store: store}
	for _, release := range catalog.Pnpm {
		plan, resolveErr := pnpmProvider.Resolve("pnpm@"+release.Version, catalog.Pnpm, false)
		if resolveErr != nil {
			return resolveErr
		}
		if plan.Resolution.Acquire {
			source := filepath.Join(artifacts, release.Filename())
			if _, err = pnpmProvider.Provision(ctx, plan, source); err != nil {
				return err
			}
		}
	}

	pythonProvider := PythonProvider{Store: store}
	for _, release := range catalog.Python {
		plan, resolveErr := pythonProvider.ResolveSelector(release.Version, catalog.Python, false)
		if resolveErr != nil {
			return resolveErr
		}
		if plan.Resolution.Acquire {
			source := filepath.Join(artifacts, release.Filename())
			if _, err = pythonProvider.Provision(ctx, plan, source); err != nil {
				return err
			}
		}
	}

	uvProvider := UVProvider{Store: store}
	for _, release := range catalog.UV {
		plan, resolveErr := uvProvider.Resolve(release.Version, catalog.UV, false)
		if resolveErr != nil {
			return resolveErr
		}
		if plan.Resolution.Acquire {
			source := filepath.Join(artifacts, release.Filename())
			if _, err = uvProvider.Provision(ctx, plan, source); err != nil {
				return err
			}
		}
	}

	rustProvider := RustProvider{Store: store}
	for _, release := range catalog.Rust {
		plan, resolveErr := rustProvider.Resolve(RustProjectRequest{
			Channel: release.Version,
			Profile: "minimal",
		}, catalog.Rust, false)
		if resolveErr != nil {
			return resolveErr
		}
		if plan.Resolution.Acquire {
			if _, err = rustProvider.Provision(ctx, plan, artifacts); err != nil {
				return err
			}
		}
	}

	goProvider := GoProvider{Store: store}
	for _, release := range catalog.Go {
		plan, resolveErr := goProvider.Resolve(GoProjectRequest{
			Minimum: release.Version, Toolchain: "go" + release.Version,
		}, catalog.Go, false)
		if resolveErr != nil {
			return resolveErr
		}
		if plan.Resolution.Acquire {
			source := filepath.Join(artifacts, release.Filename())
			if _, err = goProvider.Provision(ctx, plan, source); err != nil {
				return err
			}
		}
	}
	_, err = store.Collect(ctx, time.Now().UTC())
	return err
}
