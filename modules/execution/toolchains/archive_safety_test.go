package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Install through the public entrypoint so rejection also proves that neither
// the target generation nor its command links were published.
func installZipFixture(t *testing.T, entries []orderedZipEntry) (string, string, error) {
	t.Helper()
	bundle, root := t.TempDir(), t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(artifacts, "tool.zip")
	writeOrderedZip(t, archive, entries)
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"},
		AptPackages: []AptPackage{{Name: "git", Version: "fixture"}},
		Artifacts: []Artifact{{
			Name: "tool", Version: "1", Filename: "tool.zip",
			URL: "https://example.test/tool.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)),
			Format: "zip", InstallPath: "/opt/loki/toolchain/tool/1", Links: map[string]string{"tool": "run"},
		}},
	}
	err = withUmask(0o077, func() error { return InstallArtifacts(context.Background(), manifest, bundle, root) })
	return root, filepath.Join(root, "opt/loki/toolchain/tool/1"), err
}

func TestInstallZipRejectsUnsafePublishedTrees(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []orderedZipEntry
	}{
		{"absolute-entry", []orderedZipEntry{{name: "/escape", data: []byte("x"), mode: 0644}}},
		{"duplicate-file", []orderedZipEntry{{name: "run", data: []byte("other"), mode: 0755}}},
		{"normalized-alias", []orderedZipEntry{{name: "x/../run", data: []byte("other"), mode: 0755}}},
		{"absolute-link", []orderedZipEntry{{name: "link", data: []byte("/outside"), mode: os.ModeSymlink | 0777}}},
		{"parent-link", []orderedZipEntry{{name: "link", data: []byte("../outside"), mode: os.ModeSymlink | 0777}}},
		{"empty-link", []orderedZipEntry{{name: "link", mode: os.ModeSymlink | 0777}}},
		{"dangling-link", []orderedZipEntry{{name: "link", data: []byte("missing"), mode: os.ModeSymlink | 0777}}},
		{"cyclic-link", []orderedZipEntry{
			{name: "a", data: []byte("b"), mode: os.ModeSymlink | 0777},
			{name: "b", data: []byte("a"), mode: os.ModeSymlink | 0777},
		}},
		{"lexically-safe-escape", []orderedZipEntry{
			{name: "dir", data: []byte("."), mode: os.ModeSymlink | 0777},
			{name: "escape", data: []byte("dir/../outside"), mode: os.ModeSymlink | 0777},
		}},
		{"late-symlink-parent", []orderedZipEntry{
			{name: "dir/file", data: []byte("x"), mode: 0644},
			{name: "dir", data: []byte("."), mode: os.ModeSymlink | 0777},
		}},
		{"reserved-marker", []orderedZipEntry{{name: ".loki-artifact.json", data: []byte("run"), mode: os.ModeSymlink | 0777}}},
		{"special-object", []orderedZipEntry{{name: "pipe", mode: os.ModeNamedPipe | 0600}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := append([]orderedZipEntry{{name: "run", data: []byte("#!/bin/sh\n"), mode: 0755}}, test.entries...)
			root, target, err := installZipFixture(t, entries)
			if err == nil {
				t.Fatal("unsafe archive accepted")
			}
			for _, name := range []string{target, filepath.Join(root, "opt/loki/toolchain/bin/tool")} {
				if _, statErr := os.Lstat(name); !os.IsNotExist(statErr) {
					t.Fatalf("failed install published %s: %v", name, statErr)
				}
			}
			if names, readErr := os.ReadDir(filepath.Dir(target)); readErr == nil && len(names) != 0 {
				t.Fatalf("failed install retained staging content: %v", names)
			}
		})
	}
}

func TestInstallZipKeepsSafeLinkChainsAndReadableAncestors(t *testing.T) {
	root, target, err := installZipFixture(t, []orderedZipEntry{
		{name: "bin/", mode: os.ModeDir | 0755},
		{name: "bin/tool", data: []byte("#!/bin/sh\n"), mode: os.ModeSetuid | os.ModeSetgid | 0777},
		{name: "run", data: []byte("alias"), mode: os.ModeSymlink | 0777},
		{name: "alias", data: []byte("bin/tool"), mode: os.ModeSymlink | 0777},
		{name: "data", data: []byte("resource"), mode: 0666},
	})
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(target, "run")); err != nil || string(data) != "#!/bin/sh\n" {
		t.Fatalf("safe link chain = %q, %v", data, err)
	}
	for relative, want := range map[string]os.FileMode{
		"opt": 0755, "opt/loki": 0755, "opt/loki/toolchain": 0755,
		"opt/loki/toolchain/tool": 0755, "opt/loki/toolchain/tool/1": 0755,
		"opt/loki/toolchain/tool/1/bin": 0755, "opt/loki/toolchain/bin": 0755,
		"opt/loki/toolchain/tool/1/bin/tool": 0755, "opt/loki/toolchain/tool/1/data": 0644,
	} {
		info, err := os.Stat(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky); got != want {
			t.Fatalf("%s mode = %v, want %v", relative, got, want)
		}
	}
}

func TestNormalizeExtractedTreeRejectsResolvedEscape(t *testing.T) {
	base := t.TempDir()
	stage := filepath.Join(base, "stage")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"dir": ".", "escape": "dir/../outside"} {
		if err := os.Symlink(target, filepath.Join(stage, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := normalizeExtractedTree(stage); err == nil {
		t.Fatal("resolved escape passed final ZIP/tar tree validation")
	}
	info, err := os.Stat(outside)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("outside permissions changed: %v, %v", info, err)
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "untouched" {
		t.Fatalf("outside content changed: %q, %v", data, err)
	}
}

func TestNormalizeExtractedTreeRejectsFIFO(t *testing.T) {
	stage := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(stage, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := normalizeExtractedTree(stage); err == nil {
		t.Fatal("FIFO accepted")
	}
}

func TestInstallDirectoriesPreserveExistingPermissions(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := makeInstallDirectories(root, filepath.Join(private, "new")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(private); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("existing private directory was broadened: %v, %v", info, err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := makeInstallDirectories(root, filepath.Join(root, "link", "new")); err == nil {
		t.Fatal("symlink install ancestor accepted")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside ancestor changed: %v, %v", entries, err)
	}
}
