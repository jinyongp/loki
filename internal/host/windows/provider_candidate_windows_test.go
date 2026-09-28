//go:build windows

package windows

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/host/connect"
)

type providerFileEvidence struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Length int64  `json:"length"`
}

// Bind every byte consumed by provider acceptance to the same candidate and
// the executed frontend, not a new identity inferred from unverified inputs.
func verifyProviderCandidate(t *testing.T, tag, catalogPath, archivePath, frontendPath string) ReleaseBinding {
	t.Helper()
	root := filepath.Dir(filepath.Dir(catalogPath))
	raw, err := os.ReadFile(filepath.Join(root, "evidence.json"))
	if err != nil {
		t.Fatal(err)
	}
	var evidence struct {
		Version        int                  `json:"version"`
		SourceRevision string               `json:"source_revision"`
		Frontend       providerFileEvidence `json:"windows_frontend"`
		WSL            providerFileEvidence `json:"wsl_appliance"`
		Catalog        providerFileEvidence `json:"connect_helper_catalog"`
		Archive        providerFileEvidence `json:"connect_helper_archive"`
		License        providerFileEvidence `json:"connect_helper_license"`
		Notice         providerFileEvidence `json:"connect_helper_notice"`
		SPDX           providerFileEvidence `json:"connect_helper_spdx"`
	}
	if err = json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Version != 5 || !frontendRevisionPattern.MatchString(evidence.SourceRevision) {
		t.Fatal("provider acceptance requires CandidateEvidence v5 source identity")
	}
	if revision := os.Getenv("GITHUB_SHA"); revision != "" && revision != evidence.SourceRevision {
		t.Fatal("candidate and checked-out acceptance source revisions differ")
	}
	files := map[string]providerFileEvidence{
		"inputs/loki-windows-amd64.exe":      evidence.Frontend,
		"inputs/loki-wsl-amd64.wsl":          evidence.WSL,
		"inputs/connect-helpers.json":        evidence.Catalog,
		"inputs/connect-helper-archive.zip":  evidence.Archive,
		"inputs/connect-helper-licenses.txt": evidence.License,
		"inputs/connect-helper-NOTICE.txt":   evidence.Notice,
		"inputs/connect-helper.spdx.json":    evidence.SPDX,
	}
	for expectedPath, file := range files {
		if file.Path != expectedPath {
			t.Fatalf("candidate evidence path is not canonical for %s", expectedPath)
		}
		digest, length, err := (WindowsFrontendPlatform{}).FileDigest(filepath.Join(root, filepath.FromSlash(expectedPath)))
		if err != nil {
			t.Fatal(err)
		}
		if digest != file.SHA256 || length != file.Length {
			t.Fatalf("candidate file identity differs: %s", expectedPath)
		}
	}
	for supplied, expected := range map[string]string{
		catalogPath: evidence.Catalog.Path, archivePath: evidence.Archive.Path, frontendPath: evidence.Frontend.Path,
	} {
		if !WindowsPathEqual(supplied, filepath.Join(root, filepath.FromSlash(expected))) {
			t.Fatal("provider acceptance input is not its canonical candidate file")
		}
	}
	probe, err := (ExecNativeRunner{}).Run(t.Context(), frontendPath, []string{"version", "--json"})
	if err != nil || probe.ExitCode != 0 {
		t.Fatalf("candidate frontend version probe failed: %v (exit %d)", err, probe.ExitCode)
	}
	var version struct {
		Binding ReleaseBinding `json:"release_binding"`
	}
	if err = json.Unmarshal([]byte(probe.Stdout), &version); err != nil {
		t.Fatal(err)
	}
	binding := version.Binding
	if binding.SchemaVersion != 1 || binding.ReleaseTag != tag || binding.SourceRevision != evidence.SourceRevision ||
		binding.HelperCatalog != (FileBinding{SHA256: evidence.Catalog.SHA256, Length: evidence.Catalog.Length}) ||
		binding.WSLAppliance != (FileBinding{SHA256: evidence.WSL.SHA256, Length: evidence.WSL.Length}) {
		t.Fatal("executed frontend does not bind the accepted source/catalog/WSL candidate")
	}
	catalogRaw, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := connect.LoadCatalog(catalogRaw)
	if err != nil {
		t.Fatal(err)
	}
	helper, err := selectHelper(catalog, OpenAIHelperID, FrontendArchitecture)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct {
		asset connect.Asset
		file  providerFileEvidence
	}{
		{helper.Archive, evidence.Archive}, {helper.LicenseReport, evidence.License},
		{helper.Notice, evidence.Notice}, {helper.SPDX, evidence.SPDX},
	} {
		if pair.asset.SourceSHA256 != pair.file.SHA256 || pair.asset.SourceLength != pair.file.Length {
			t.Fatalf("reviewed helper sidecar/archive pin differs: %s", pair.file.Path)
		}
	}
	archive, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, sidecar := range []struct {
		member string
		file   providerFileEvidence
	}{
		{"NOTICE", evidence.Notice},
		{"tunnel-client-v" + helper.Version + "-windows-amd64-licenses.txt", evidence.License},
		{"tunnel-client-v" + helper.Version + "-windows-amd64.spdx.json", evidence.SPDX},
	} {
		found := false
		for _, member := range archive.File {
			if member.Name != sidecar.member {
				continue
			}
			if found {
				t.Fatal("duplicate embedded compliance sidecar")
			}
			found = true
			stream, err := member.Open()
			if err != nil {
				t.Fatal(err)
			}
			embedded, readErr := io.ReadAll(io.LimitReader(stream, sidecar.file.Length+1))
			closeErr := stream.Close()
			if readErr != nil || closeErr != nil {
				t.Fatal("embedded compliance sidecar cannot be read")
			}
			standalone, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(sidecar.file.Path)))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(embedded, standalone) {
				t.Fatalf("embedded sidecar differs: %s", sidecar.member)
			}
		}
		if !found {
			t.Fatalf("embedded sidecar missing: %s", sidecar.member)
		}
	}
	if !strings.HasPrefix(helper.Archive.MirrorAsset, "loki-helper-") {
		t.Fatal("reviewed helper does not have a Loki publication asset identity")
	}
	return binding
}
