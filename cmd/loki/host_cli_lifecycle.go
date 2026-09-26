package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"loki/internal/host/lifecycle"
)

type managedHostCLIReleaseAssets struct {
	System bool
}

func managedHostCLIReleaseAssetsForStore(ctx context.Context, store *lifecycle.FileStore) (*managedHostCLIReleaseAssets, error) {
	if store == nil {
		return nil, errors.New("host lifecycle store is not configured")
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Installation == nil || !snapshot.Installation.Valid() {
		return nil, errors.New("host lifecycle installation state is missing")
	}
	return &managedHostCLIReleaseAssets{System: snapshot.Installation.Scope == "system"}, nil
}

func (m *managedHostCLIReleaseAssets) ValidateCurrent(ctx context.Context, generation lifecycle.Generation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, err := resolveHostCLIInstallPaths(m != nil && m.System, generation.ID)
	if err != nil {
		return err
	}
	target, err := managedHostCLILinkTarget(paths)
	if err != nil {
		return err
	}
	if target != filepath.Clean(paths.Binary) {
		return errors.New("managed host CLI link does not match the installed release")
	}
	return verifyFileDigest(paths.Binary, generation.Spec.HostBinaryDigest)
}

func (m *managedHostCLIReleaseAssets) Activate(ctx context.Context, generation lifecycle.Generation) error {
	return m.switchTo(ctx, generation)
}

func (m *managedHostCLIReleaseAssets) Restore(ctx context.Context, generation lifecycle.Generation) error {
	return m.switchTo(ctx, generation)
}

func (m *managedHostCLIReleaseAssets) switchTo(ctx context.Context, generation lifecycle.Generation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	paths, err := resolveHostCLIInstallPaths(m != nil && m.System, generation.ID)
	if err != nil {
		return err
	}
	if err = preflightHostCLIInstall(paths); err != nil {
		return err
	}
	if err = verifyFileDigest(paths.Binary, generation.Spec.HostBinaryDigest); err != nil {
		return err
	}
	if err = publishManagedCLILink(paths); err != nil {
		return err
	}
	return ctx.Err()
}

func managedHostCLILinkTarget(paths hostCLIInstallPaths) (string, error) {
	info, err := os.Lstat(paths.Link)
	if errors.Is(err, os.ErrNotExist) {
		return "", errors.New("managed host CLI link is missing")
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("managed host CLI link is not a symlink")
	}
	target, err := os.Readlink(paths.Link)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(paths.Link), target)
	}
	target = filepath.Clean(target)
	if !pathContains(filepath.Clean(paths.ReleaseRoot), target) {
		return "", errors.New("managed host CLI link escapes the managed release root")
	}
	return target, nil
}
