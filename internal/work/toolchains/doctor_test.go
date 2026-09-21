package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorIsOfflineAndDetectsTampering(t *testing.T) {
	bundle, root := t.TempDir(), t.TempDir()
	payload := []byte("tool")
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	if err := os.MkdirAll(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "artifacts", "tool"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: 1, Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"}, AptPackages: []AptPackage{{Name: "git", Version: "1"}}, Artifacts: []Artifact{{Name: "tool", Version: "1", Filename: "tool", URL: "https://example.test/tool", SHA256: sum, Format: "file", InstallPath: "/opt/loki/toolchain/tool/1/tool", Links: map[string]string{"tool": "."}}}}
	if err := InstallArtifacts(context.Background(), manifest, bundle, root); err != nil {
		t.Fatal(err)
	}
	if report := Doctor(context.Background(), manifest, root, false); !report.OK {
		t.Fatalf("doctor = %#v", report)
	}
	if err := os.WriteFile(filepath.Join(root, "opt", "loki", "toolchain", "tool", "1", "tool"), []byte("changed"), 0755); err != nil {
		t.Fatal(err)
	}
	if report := Doctor(context.Background(), manifest, root, false); report.OK {
		t.Fatal("tampering not detected")
	}
}
