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
)

const CatalogVersion = 1

type Catalog struct {
	Version int           `json:"version"`
	Node    []NodeRelease `json:"node"`
	Pnpm    []PnpmRelease `json:"pnpm"`
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
	if len(c.Node) == 0 {
		return errors.New("toolchain catalog has no Node.js releases")
	}
	if err := validateNodeCatalog(c.Node); err != nil {
		return err
	}
	if err := validatePnpmCatalog(c.Pnpm); err != nil {
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

func ProvisionCatalog(ctx context.Context, catalog Catalog, bundle string, store GenerationStore) error {
	if err := catalog.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(bundle) || filepath.Clean(bundle) != bundle {
		return errors.New("toolchain catalog bundle must be a clean absolute path")
	}
	artifacts := filepath.Join(bundle, "artifacts")
	info, err := os.Lstat(artifacts)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("toolchain catalog artifact directory is unavailable")
	}

	nodeProvider := NodeProvider{Store: store}
	nodePlans := make(map[string]NodePlan, len(catalog.Node))
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
		nodePlans[release.Version] = plan
	}

	pnpmProvider := PnpmProvider{Store: store}
	for _, release := range catalog.Pnpm {
		node, selectErr := highestCompatibleNodePlan(release, catalog.Node, nodePlans)
		if selectErr != nil {
			return selectErr
		}
		plan, resolveErr := pnpmProvider.Resolve("pnpm@"+release.Version, catalog.Pnpm, node, false)
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
	return nil
}

func highestCompatibleNodePlan(release PnpmRelease, releases []NodeRelease, plans map[string]NodePlan) (NodePlan, error) {
	var selected NodeRelease
	found := false
	scheme := NodeVersionScheme{}
	for _, candidate := range releases {
		compatible, err := release.SupportsNode(candidate.Version)
		if err != nil {
			return NodePlan{}, err
		}
		if !compatible {
			continue
		}
		if !found {
			selected = candidate
			found = true
			continue
		}
		comparison, err := scheme.Compare(candidate.Version, selected.Version)
		if err != nil {
			return NodePlan{}, err
		}
		if comparison > 0 {
			selected = candidate
		}
	}
	if !found {
		return NodePlan{}, fmt.Errorf("pnpm %s has no compatible Node.js release in the catalog", release.Version)
	}
	plan, ok := plans[selected.Version]
	if !ok {
		return NodePlan{}, errors.New("compatible Node.js generation was not provisioned")
	}
	return plan, nil
}
