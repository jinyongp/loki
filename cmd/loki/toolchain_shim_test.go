package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeShimExecutable(t *testing.T, root, family, version, command string) string {
	t.Helper()
	var path string
	switch family {
	case "node":
		path = filepath.Join(root, family, "opt", "loki", "toolchain", family, version, "bin", command)
	case "pnpm":
		path = filepath.Join(root, family, "opt", "loki", "toolchain", family, version, command)
	default:
		t.Fatalf("unsupported fixture family %q", family)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0555); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveToolchainShimUsesSingleMountedVersion(t *testing.T) {
	root := t.TempDir()
	node := writeShimExecutable(t, root, "node", "26.9.0", "node")
	writeShimExecutable(t, root, "node", "26.9.0", "npm")
	writeShimExecutable(t, root, "node", "26.9.0", "npx")
	pnpm := writeShimExecutable(t, root, "pnpm", "12.5.1", "pnpm")

	for command, want := range map[string]string{
		"node": node,
		"pnpm": pnpm,
	} {
		got, err := resolveToolchainShim(root, command)
		if err != nil || got != want {
			t.Fatalf("resolve %s = %q, %v; want %q", command, got, err, want)
		}
	}
	if toolchainShimCommand("/opt/loki/toolchain/bin/node") != "node" ||
		toolchainShimCommand("/opt/loki/bin/loki") != "" {
		t.Fatal("shim argv0 detection is invalid")
	}
}

func TestResolveToolchainShimRejectsAmbiguousOrInvalidMounts(t *testing.T) {
	root := t.TempDir()
	writeShimExecutable(t, root, "node", "26.9.0", "node")
	writeShimExecutable(t, root, "node", "22.23.4", "node")
	if _, err := resolveToolchainShim(root, "node"); err == nil || !strings.Contains(err.Error(), "multiple versions") {
		t.Fatalf("ambiguous Node mount error = %v", err)
	}

	root = t.TempDir()
	writeShimExecutable(t, root, "node", "v26.9.0", "node")
	if _, err := resolveToolchainShim(root, "node"); err == nil || !strings.Contains(err.Error(), "version is invalid") {
		t.Fatalf("invalid Node mount error = %v", err)
	}
	if _, err := resolveToolchainShim(root, "corepack"); err == nil {
		t.Fatal("unsupported shim command was accepted")
	}
}
