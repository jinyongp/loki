package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	applauncher "loki/internal/app/launcher"
	"loki/internal/platform/sandbox"
	"loki/internal/toolchain"
	"loki/internal/work/jobs"
)

type launcherToolchainResolver struct {
	store toolchain.GenerationStore
}

func (r launcherToolchainResolver) Resolve(ctx context.Context, refs []jobs.ToolchainRef) (applauncher.ResolvedToolchains, error) {
	if len(refs) == 0 {
		return applauncher.ResolvedToolchains{}, nil
	}
	mounts := make([]sandbox.ToolchainMount, 0, len(refs))
	leases := make([]*toolchain.GenerationLease, 0, len(refs))
	release := func() error {
		var err error
		for index := len(leases) - 1; index >= 0; index-- {
			err = errors.Join(err, leases[index].Release())
		}
		leases = nil
		return err
	}
	for _, ref := range refs {
		generation, err := r.store.Lookup(ref.GenerationID)
		if err != nil {
			_ = release()
			return applauncher.ResolvedToolchains{}, fmt.Errorf("resolve %s toolchain generation: %w", ref.Family, err)
		}
		if err = validateToolchainGeneration(ref, generation); err != nil {
			_ = release()
			return applauncher.ResolvedToolchains{}, err
		}
		lease, err := r.store.Acquire(ref.GenerationID)
		if err != nil {
			_ = release()
			return applauncher.ResolvedToolchains{}, fmt.Errorf("lease %s toolchain generation: %w", ref.Family, err)
		}
		leases = append(leases, lease)
		mounts = append(mounts, sandbox.ToolchainMount{Family: ref.Family, Source: generation.Root})
		select {
		case <-ctx.Done():
			_ = release()
			return applauncher.ResolvedToolchains{}, ctx.Err()
		default:
		}
	}
	return applauncher.ResolvedToolchains{Mounts: mounts, Release: release}, nil
}

func validateToolchainGeneration(ref jobs.ToolchainRef, generation toolchain.Generation) error {
	var paths []string
	switch ref.Family {
	case "node":
		if normalized, err := (toolchain.NodeVersionScheme{}).NormalizeVersion(ref.Version); err != nil || normalized != ref.Version {
			return errors.New("Node.js Job toolchain version is invalid")
		}
		base := filepath.Join(generation.Root, "opt", "loki", "toolchain", "node", ref.Version, "bin")
		paths = []string{filepath.Join(base, "node"), filepath.Join(base, "npm"), filepath.Join(base, "npx")}
	case "pnpm":
		if normalized, err := (toolchain.PnpmVersionScheme{}).NormalizeVersion(ref.Version); err != nil || normalized != ref.Version {
			return errors.New("pnpm Job toolchain version is invalid")
		}
		paths = []string{filepath.Join(generation.Root, "opt", "loki", "toolchain", "pnpm", ref.Version, "pnpm")}
	default:
		return fmt.Errorf("unsupported Job toolchain family %q", ref.Family)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("%s Job toolchain generation is missing executable %s", ref.Family, filepath.Base(path))
		}
	}
	return nil
}
