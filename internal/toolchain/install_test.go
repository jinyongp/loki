package toolchain

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
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
	bundle, root := t.TempDir(), t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(artifacts, "browser.zip")
	writeZip(t, archive, map[string]struct {
		data []byte
		mode os.FileMode
	}{"browser/chrome": {data: []byte("#!/bin/sh\n"), mode: 0755}, "browser/resources.pak": {data: []byte("data"), mode: 0644}})
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "browser", Version: "1", Filename: "browser.zip", URL: "https://example.test/browser.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(payload)), Format: "zip", InstallPath: "/opt/loki/toolchain/browser/1", StripComponents: 1, Links: map[string]string{"browser": "chrome"}}}}
	if err = InstallArtifacts(context.Background(), manifest, bundle, root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "opt", "loki", "toolchain", "browser", "1", "chrome")
	if info, statErr := os.Stat(target); statErr != nil || info.Mode().Perm() != 0755 {
		t.Fatalf("installed executable = %v, %v", info, statErr)
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
