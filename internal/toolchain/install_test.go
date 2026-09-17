package toolchain

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func writeZip(t *testing.T, name string, entries map[string]struct {
	data []byte
	mode os.FileMode
}) {
	t.Helper()
	output, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(output)
	for path, entry := range entries {
		header := &zip.FileHeader{Name: path, Method: zip.Store}
		header.SetMode(entry.mode)
		writer, createErr := archive.CreateHeader(header)
		if createErr == nil {
			_, createErr = writer.Write(entry.data)
		}
		if createErr != nil {
			t.Fatal(createErr)
		}
	}
	if err = archive.Close(); err == nil {
		err = output.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

type orderedZipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func writeOrderedZip(t *testing.T, name string, entries []orderedZipEntry) {
	t.Helper()
	output, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetMode(entry.mode)
		writer, createErr := archive.CreateHeader(header)
		if createErr == nil {
			_, createErr = writer.Write(entry.data)
		}
		if createErr != nil {
			t.Fatal(createErr)
		}
	}
	if err = archive.Close(); err == nil {
		err = output.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

type tarEntry struct {
	name, link string
	data       []byte
	mode       int64
	typeflag   byte
}

func writeTarGzip(t *testing.T, name string, entries []tarEntry) {
	t.Helper()
	output, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(output)
	archive := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Linkname: entry.link, Mode: entry.mode,
			Typeflag: entry.typeflag, Size: int64(len(entry.data)),
		}
		if entry.typeflag != tar.TypeReg && entry.typeflag != tar.TypeRegA {
			header.Size = 0
		}
		if err = archive.WriteHeader(header); err == nil && len(entry.data) > 0 {
			_, err = archive.Write(entry.data)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = archive.Close(); err == nil {
		err = gzipWriter.Close()
	}
	if err == nil {
		err = output.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
}

func withUmask(mask int, run func() error) error {
	previous := unix.Umask(mask)
	defer unix.Umask(previous)
	return run()
}

func TestInstallFileArtifactIsIdempotent(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	payload := []byte("#!/bin/sh\necho test\n")
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	if err := os.Mkdir(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "artifacts", "tool"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "tool", Version: "1", Filename: "tool", URL: "https://example.test/tool", SHA256: sum, Format: "file", InstallPath: "/opt/loki/toolchain/tool/1/tool", Links: map[string]string{"tool": "."}}}}
	for range 2 {
		if err := InstallArtifacts(context.Background(), manifest, bundle, root); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(root, "opt", "loki", "toolchain", "tool", "1", "tool")
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("installed file = %v, %v", info, err)
	}
	link := filepath.Join(root, "opt", "loki", "toolchain", "bin", "tool")
	if got, err := os.Readlink(link); err != nil || got != "/opt/loki/toolchain/tool/1/tool" {
		t.Fatalf("link = %q, %v", got, err)
	}
	if err := os.WriteFile(target, []byte("tampered"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := InstallArtifacts(context.Background(), manifest, bundle, root); err == nil {
		t.Fatal("tampered installed artifact accepted")
	}
}

func TestInstallRejectsChecksumAndOccupiedPath(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "artifacts", "tool"), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "tool", Version: "1", Filename: "tool", URL: "https://example.test/tool", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Format: "file", InstallPath: "/opt/loki/toolchain/tool/1/tool", Links: map[string]string{"tool": "."}}}}
	if err := InstallArtifacts(context.Background(), manifest, bundle, root); err == nil {
		t.Fatal("bad checksum accepted")
	}
	manifest.Artifacts[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("bad")))
	target := filepath.Join(root, "opt", "loki", "toolchain", "tool", "1", "tool")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("occupied"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := InstallArtifacts(context.Background(), manifest, bundle, root); err == nil {
		t.Fatal("occupied path overwritten")
	}
}

func TestInstallZipArtifactPreservesExecutables(t *testing.T) {
	for _, mask := range []int{0o022, 0o077} {
		t.Run(fmt.Sprintf("umask-%03o", mask), func(t *testing.T) {
			bundle, root := t.TempDir(), t.TempDir()
			artifacts := filepath.Join(bundle, "artifacts")
			if err := os.Mkdir(artifacts, 0755); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(artifacts, "browser.zip")
			writeZip(t, archive, map[string]struct {
				data []byte
				mode os.FileMode
			}{
				"browser/bin/chrome":    {data: []byte("#!/bin/sh\n"), mode: 0755},
				"browser/resources.pak": {data: []byte("data"), mode: 0644},
			})
			payload, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "browser", Version: "1", Filename: "browser.zip", URL: "https://example.test/browser.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Format: "zip", InstallPath: "/opt/loki/toolchain/browser/1", StripComponents: 1, Links: map[string]string{"browser": "bin/chrome"}}}}
			if err = withUmask(mask, func() error { return InstallArtifacts(context.Background(), manifest, bundle, root) }); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "opt", "loki", "toolchain", "browser", "1", "bin", "chrome")
			for path, mode := range map[string]os.FileMode{
				target: 0755,
				filepath.Join(filepath.Dir(filepath.Dir(target)), "resources.pak"): 0644,
				filepath.Dir(target):               0755,
				filepath.Dir(filepath.Dir(target)): 0755,
			} {
				if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != mode {
					t.Fatalf("mode %s = %v, %v; want %04o", path, info, statErr, mode)
				}
			}
			if err = os.Chmod(filepath.Dir(filepath.Dir(target)), 0700); err != nil {
				t.Fatal(err)
			}
			if err = withUmask(mask, func() error { return InstallArtifacts(context.Background(), manifest, bundle, root) }); err != nil {
				t.Fatal(err)
			}
			if info, statErr := os.Stat(filepath.Dir(filepath.Dir(target))); statErr != nil || info.Mode().Perm() != 0755 {
				t.Fatalf("repaired artifact root = %v, %v", info, statErr)
			}
		})
	}
}

func TestInstallZipRejectsPathEscape(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(artifacts, "browser.zip")
	writeZip(t, archive, map[string]struct {
		data []byte
		mode os.FileMode
	}{"browser/../../escape": {data: []byte("bad"), mode: 0644}})
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "browser", Version: "1", Filename: "browser.zip", URL: "https://example.test/browser.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Format: "zip", InstallPath: "/opt/loki/toolchain/browser/1", StripComponents: 1, Links: map[string]string{"browser": "chrome"}}}}
	if err = InstallArtifacts(context.Background(), manifest, bundle, root); err == nil {
		t.Fatal("zip path escape accepted")
	}
	if _, err = os.Stat(filepath.Join(root, "opt", "loki", "toolchain", "browser", "escape")); !os.IsNotExist(err) {
		t.Fatal("zip path escape created output")
	}
}

func TestInstallZipRejectsChainedSymlinkEscape(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(artifacts, "fixture.zip")
	writeOrderedZip(t, archive, []orderedZipEntry{
		{name: "dir", data: []byte("."), mode: os.ModeSymlink | 0777},
		{name: "dir/escape", data: []byte("../outside"), mode: os.ModeSymlink | 0777},
		{name: "escape/probe.txt", data: []byte("synthetic marker\n"), mode: 0644},
	})
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "opt", "loki", "toolchain", "review", "outside")
	if err = os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "review", Version: "1", Filename: "fixture.zip", URL: "https://example.test/fixture.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Format: "zip", InstallPath: "/opt/loki/toolchain/review/1", Links: map[string]string{"review": "probe.txt"}}}}
	if err = InstallArtifacts(context.Background(), manifest, bundle, root); err == nil {
		t.Fatal("chained zip symlink escape accepted")
	}
	if _, err = os.Stat(filepath.Join(outside, "probe.txt")); !os.IsNotExist(err) {
		t.Fatal("chained zip symlink escape wrote outside staging root")
	}
}

func TestInstallZipPreservesSafeRelativeSymlink(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(artifacts, "fixture.zip")
	writeOrderedZip(t, archive, []orderedZipEntry{
		{name: "browser/bin/tool", data: []byte("#!/bin/sh\n"), mode: 0755},
		{name: "browser/tool", data: []byte("bin/tool"), mode: os.ModeSymlink | 0777},
	})
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "browser", Version: "1", Filename: "fixture.zip", URL: "https://example.test/fixture.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Format: "zip", InstallPath: "/opt/loki/toolchain/browser/1", StripComponents: 1, Links: map[string]string{"browser": "tool"}}}}
	if err = InstallArtifacts(context.Background(), manifest, bundle, root); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "opt", "loki", "toolchain", "browser", "1", "tool")
	if target, readErr := os.Readlink(link); readErr != nil || target != "bin/tool" {
		t.Fatalf("safe archive symlink = %q, %v", target, readErr)
	}
}

func TestInstallTarArchiveNormalizesModes(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(artifacts, "tool.tar.gz")
	writeTarGzip(t, archive, []tarEntry{
		{name: "tool/bin/run", data: []byte("#!/bin/sh\n"), mode: 0755, typeflag: tar.TypeReg},
		{name: "tool/share/data", data: []byte("data\n"), mode: 0644, typeflag: tar.TypeReg},
	})
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "tool", Version: "1", Filename: "tool.tar.gz", URL: "https://example.test/tool.tar.gz", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Format: "tar.gz", InstallPath: "/opt/loki/toolchain/tool/1", StripComponents: 1, Links: map[string]string{"tool": "bin/run"}}}}
	if err = withUmask(0o077, func() error { return InstallArtifacts(context.Background(), manifest, bundle, root) }); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "opt", "loki", "toolchain", "tool", "1")
	for path, mode := range map[string]os.FileMode{
		base:                              0755,
		filepath.Join(base, "bin"):        0755,
		filepath.Join(base, "bin/run"):    0755,
		filepath.Join(base, "share"):      0755,
		filepath.Join(base, "share/data"): 0644,
	} {
		if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s = %v, %v; want %04o", path, info, statErr, mode)
		}
	}
}

func TestInstallTarRejectsUnsafeSymlinkTarget(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(artifacts, "tool.tar.gz")
	writeTarGzip(t, archive, []tarEntry{
		{name: "tool/bin/run", data: []byte("#!/bin/sh\n"), mode: 0755, typeflag: tar.TypeReg},
		{name: "tool/escape", link: "../../outside", mode: 0777, typeflag: tar.TypeSymlink},
	})
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "tool", Version: "1", Filename: "tool.tar.gz", URL: "https://example.test/tool.tar.gz", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Format: "tar.gz", InstallPath: "/opt/loki/toolchain/tool/1", StripComponents: 1, Links: map[string]string{"tool": "bin/run"}}}}
	if err = InstallArtifacts(context.Background(), manifest, bundle, root); err == nil {
		t.Fatal("unsafe tar symlink target accepted")
	}
	if _, err = os.Lstat(filepath.Join(root, "opt", "loki", "toolchain", "tool", "1")); !os.IsNotExist(err) {
		t.Fatal("unsafe tar artifact was published")
	}
}
