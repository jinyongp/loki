package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	applauncher "loki/internal/app/launcher"
	"loki/internal/platform/sandbox"
	"loki/internal/work/jobs"
	"loki/internal/work/toolchains"
)

type launcherToolchainResolver struct {
	store toolchain.GenerationStore
}

func (r launcherToolchainResolver) Resolve(ctx context.Context, owner string, refs []jobs.ToolchainRef) (applauncher.ResolvedToolchains, error) {
	if len(refs) == 0 {
		return applauncher.ResolvedToolchains{}, nil
	}
	mounts := make([]sandbox.ToolchainMount, 0, len(refs))
	leases := make([]*toolchain.GenerationLease, 0, len(refs))
	finish := func(remove bool) error {
		var err error
		for index := len(leases) - 1; index >= 0; index-- {
			if remove {
				err = errors.Join(err, leases[index].Release())
			} else {
				err = errors.Join(err, leases[index].Close())
			}
		}
		leases = nil
		return err
	}
	for _, ref := range refs {
		generation, err := r.store.Lookup(ref.GenerationID)
		if err != nil {
			_ = finish(true)
			return applauncher.ResolvedToolchains{}, fmt.Errorf("resolve %s toolchain generation: %w", ref.Family, err)
		}
		if err = validateToolchainGeneration(ref, generation); err != nil {
			_ = finish(true)
			return applauncher.ResolvedToolchains{}, err
		}
		lease, err := r.store.Acquire(ref.GenerationID, owner)
		if err != nil {
			_ = finish(true)
			return applauncher.ResolvedToolchains{}, fmt.Errorf("lease %s toolchain generation: %w", ref.Family, err)
		}
		leases = append(leases, lease)
		mounts = append(mounts, sandbox.ToolchainMount{Family: ref.Family, Source: generation.Root})
		select {
		case <-ctx.Done():
			_ = finish(true)
			return applauncher.ResolvedToolchains{}, ctx.Err()
		default:
		}
	}
	return applauncher.ResolvedToolchains{
		Mounts:  mounts,
		Close:   func() error { return finish(false) },
		Discard: func() error { return finish(true) },
	}, nil
}

func (r launcherToolchainResolver) Cleanup(owner string, refs []jobs.ToolchainRef) error {
	var err error
	for _, ref := range refs {
		err = errors.Join(err, r.store.ReleaseOwner(ref.GenerationID, owner))
	}
	return err
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
	case "python":
		if normalized, err := (toolchain.PythonVersionScheme{}).NormalizeVersion(ref.Version); err != nil || normalized != ref.Version {
			return errors.New("Python Job toolchain version is invalid")
		}
		parts := strings.Split(ref.Version, ".")
		base := filepath.Join(generation.Root, "opt", "loki", "toolchain", "python", ref.Version, "bin")
		paths = []string{filepath.Join(base, "python3"), filepath.Join(base, "python"+strings.Join(parts[:2], "."))}
	case "uv":
		if normalized, err := (toolchain.UVVersionScheme{}).NormalizeVersion(ref.Version); err != nil || normalized != ref.Version {
			return errors.New("uv Job toolchain version is invalid")
		}
		base := filepath.Join(generation.Root, "opt", "loki", "toolchain", "uv", ref.Version)
		paths = []string{filepath.Join(base, "uv"), filepath.Join(base, "uvx")}
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
