package windows

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func bindingForBytes(raw []byte) ReleaseBinding {
	sum := sha256.Sum256(raw)
	return ReleaseBinding{
		ReleaseTag:   "v0.1.20",
		WSLAppliance: FileBinding{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(raw))},
	}
}

func TestReleaseAppliancePreparerLocalExactBytes(t *testing.T) {
	raw := []byte("wsl-appliance")
	root := t.TempDir()
	source := filepath.Join(root, "fixture.wsl")
	if err := os.WriteFile(source, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, cleanup, err := (ReleaseAppliancePreparer{Binding: bindingForBytes(raw)}).PrepareAppliance(
		t.Context(), InstallOptions{LocalAppliance: source},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	got, err := os.ReadFile(prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("prepared bytes=%q", got)
	}
}

func TestReleaseAppliancePreparerRejectsLocalSymlinkAndDigestDrift(t *testing.T) {
	raw := []byte("wsl-appliance")
	root := t.TempDir()
	source := filepath.Join(root, "fixture.wsl")
	if err := os.WriteFile(source, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "fixture-link.wsl")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	preparer := ReleaseAppliancePreparer{Binding: bindingForBytes(raw)}
	if _, cleanup, err := preparer.PrepareAppliance(t.Context(), InstallOptions{LocalAppliance: link}); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("symlink appliance was accepted")
	}
	bad := bindingForBytes([]byte("different"))
	bad.WSLAppliance.Length = int64(len(raw))
	if _, cleanup, err := (ReleaseAppliancePreparer{Binding: bad}).PrepareAppliance(t.Context(), InstallOptions{LocalAppliance: source}); err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("digest drift was accepted")
	}
}

func TestDownloadVerifiedApplianceRequiresExactBytes(t *testing.T) {
	raw := []byte("downloaded-wsl")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	target := filepath.Join(t.TempDir(), "download.wsl")
	if err := downloadVerifiedAppliance(t.Context(), server.Client(), server.URL, target, bindingForBytes(raw).WSLAppliance); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != string(raw) {
		t.Fatalf("download=%q err=%v", got, err)
	}
}
