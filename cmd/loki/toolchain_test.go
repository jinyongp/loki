package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/toolchain"
)

func TestToolchainInstallAndOfflineDoctor(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	payload := []byte("tool")
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	manifest := toolchain.Manifest{Version: 1, Platform: toolchain.Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []toolchain.AptPackage{{Name: "git", Version: "1"}}, Artifacts: []toolchain.Artifact{{Name: "tool", Version: "1", Filename: "tool", URL: "https://example.test/tool", SHA256: sum, Format: "file", InstallPath: "/opt/loki/toolchain/tool/1/tool", Links: map[string]string{"tool": "."}}}}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bundle, "manifest.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bundle, "artifacts", "tool"), payload, 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("ID=ubuntu\nVERSION_ID=24.04\n"), 0644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runToolchain([]string{"install", "--bundle", bundle, "--root", root, "--skip-apt"}, &stdout, &stderr); code != 0 {
		t.Fatalf("install = %d, %s", code, stderr.String())
	}
	for _, name := range []string{filepath.Join(root, "usr", "share", "doc", "loki", "toolchain-manifest.json"), filepath.Join(root, "opt", "loki", "toolchain", "provenance.json")} {
		if _, err = os.Stat(name); err != nil {
			t.Fatal(err)
		}
	}
	if code := runToolchain([]string{"doctor", "--manifest", filepath.Join(bundle, "manifest.json"), "--root", root, "--skip-apt"}, &stdout, &stderr); code != 0 {
		t.Fatalf("doctor = %d, %s", code, stderr.String())
	}
	var report toolchain.Report
	if err = json.Unmarshal(stdout.Bytes(), &report); err != nil || !report.OK {
		t.Fatalf("report = %#v, %v", report, err)
	}
}
