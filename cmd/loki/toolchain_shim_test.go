package main

import (
	"os"
	"path/filepath"
	"reflect"
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
	case "python":
		path = filepath.Join(root, family, "opt", "loki", "toolchain", family, version, "bin", "python3")
	case "uv":
		path = filepath.Join(root, family, "opt", "loki", "toolchain", family, version, command)
	case "rust":
		path = filepath.Join(root, family, "opt", "loki", "toolchain", family, version, "active", "bin", command)
	case "go":
		path = filepath.Join(root, family, "opt", "loki", "toolchain", family, version, "bin", command)
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
	python := writeShimExecutable(t, root, "python", "3.14.7", "python")
	uv := writeShimExecutable(t, root, "uv", "0.12.17", "uv")
	uvx := writeShimExecutable(t, root, "uv", "0.12.17", "uvx")
	rustc := writeShimExecutable(t, root, "rust", "1.98.1", "rustc")
	cargo := writeShimExecutable(t, root, "rust", "1.98.1", "cargo")
	rustfmt := writeShimExecutable(t, root, "rust", "1.98.1", "rustfmt")
	goBin := writeShimExecutable(t, root, "go", "1.27.1", "go")
	gofmt := writeShimExecutable(t, root, "go", "1.27.1", "gofmt")

	for command, want := range map[string]string{
		"node":    node,
		"pnpm":    pnpm,
		"python":  python,
		"python3": python,
		"uv":      uv,
		"uvx":     uvx,
		"rustc":   rustc,
		"cargo":   cargo,
		"rustfmt": rustfmt,
		"go":      goBin,
		"gofmt":   gofmt,
	} {
		got, err := resolveToolchainShim(root, command)
		if err != nil || got != want {
			t.Fatalf("resolve %s = %q, %v; want %q", command, got, err, want)
		}
	}
	if toolchainShimCommand("/opt/loki/toolchain/bin/node") != "node" ||
		toolchainShimCommand("/opt/loki/toolchain/bin/python") != "python" ||
		toolchainShimCommand("/opt/loki/toolchain/bin/uv") != "uv" ||
		toolchainShimCommand("/opt/loki/toolchain/bin/rustc") != "rustc" ||
		toolchainShimCommand("/opt/loki/toolchain/bin/go") != "go" ||
		toolchainShimCommand("/opt/loki/bin/loki") != "" {
		t.Fatal("shim argv0 detection is invalid")
	}
}

func TestGoToolchainShimForcesManagedLocalSelection(t *testing.T) {
	environment := []string{"PATH=/bin", "GOTOOLCHAIN=auto", "HOME=/tmp/home"}
	got := toolchainShimEnvironment("go", environment)
	want := []string{"PATH=/bin", "HOME=/tmp/home", "GOTOOLCHAIN=local"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go shim environment = %#v, want %#v", got, want)
	}
	if other := toolchainShimEnvironment("node", environment); !reflect.DeepEqual(other, environment) {
		t.Fatalf("non-Go shim environment changed = %#v", other)
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
