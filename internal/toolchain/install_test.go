package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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
