package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"loki/internal/work/toolchains"
)

const managedToolchainRoot = "/opt/loki/managed"

func toolchainShimCommand(argv0 string) string {
	switch filepath.Base(argv0) {
	case "node", "npm", "npx", "pnpm", "python", "python3", "uv", "uvx":
		return filepath.Base(argv0)
	default:
		return ""
	}
}

func resolveToolchainShim(root, command string) (string, error) {
	var family string
	switch command {
	case "node", "npm", "npx":
		family = "node"
	case "pnpm":
		family = "pnpm"
	case "python", "python3":
		family = "python"
	case "uv", "uvx":
		family = "uv"
	default:
		return "", errors.New("unsupported Loki toolchain shim")
	}
	familyRoot := filepath.Join(root, family, "opt", "loki", "toolchain", family)
	entries, err := os.ReadDir(familyRoot)
	if err != nil {
		return "", fmt.Errorf("%s toolchain is not mounted: %w", family, err)
	}
	var version string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if version != "" {
			return "", fmt.Errorf("%s toolchain mount contains multiple versions", family)
		}
		version = entry.Name()
	}
	if version == "" {
		return "", fmt.Errorf("%s toolchain mount contains no version", family)
	}
	switch family {
	case "node":
		normalized, err := (toolchain.NodeVersionScheme{}).NormalizeVersion(version)
		if err != nil || normalized != version {
			return "", errors.New("mounted Node.js version is invalid")
		}
	case "pnpm":
		normalized, err := (toolchain.PnpmVersionScheme{}).NormalizeVersion(version)
		if err != nil || normalized != version {
			return "", errors.New("mounted pnpm version is invalid")
		}
	case "python":
		normalized, err := (toolchain.PythonVersionScheme{}).NormalizeVersion(version)
		if err != nil || normalized != version {
			return "", errors.New("mounted Python version is invalid")
		}
	case "uv":
		normalized, err := (toolchain.UVVersionScheme{}).NormalizeVersion(version)
		if err != nil || normalized != version {
			return "", errors.New("mounted uv version is invalid")
		}
	}
	target := filepath.Join(familyRoot, version)
	switch family {
	case "node":
		target = filepath.Join(target, "bin", command)
	case "pnpm":
		target = filepath.Join(target, "pnpm")
	case "python":
		target = filepath.Join(target, "bin", "python3")
	case "uv":
		target = filepath.Join(target, command)
	}
	info, err := os.Stat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("managed %s executable is unavailable", command)
	}
	return target, nil
}

func runToolchainShim(command string, args []string, stderr io.Writer) int {
	target, err := resolveToolchainShim(managedToolchainRoot, command)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 126
	}
	argv := append([]string{command}, args...)
	if err = unix.Exec(target, argv, os.Environ()); err != nil {
		fmt.Fprintln(stderr, err)
		return 126
	}
	return 0
}
