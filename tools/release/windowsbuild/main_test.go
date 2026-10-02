package main

import (
	"bytes"
	"context"
	"debug/pe"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/host/connect"
)

func TestReleaseFrontendEmbedsConsoleFreeKeepalive(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "loki.exe")
	err = (execBuildRunner{}).Build(t.Context(), buildRequest{
		SourceRoot: root, Output: output, Version: "1.2.3", SourceRevision: strings.Repeat("a", 40), ReleasedAt: "2026-10-03T00:00:00Z",
		WSL: fileIdentity{SHA256: strings.Repeat("b", 64), Length: 1}, HelperCatalog: fileIdentity{SHA256: strings.Repeat("c", 64), Length: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	image, err := pe.NewFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	if image.OptionalHeader.(*pe.OptionalHeader64).Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_CUI {
		t.Fatal("frontend lost console CLI behavior")
	}
	index := bytes.Index(raw, []byte("TVqQAAMAAAAEAAAA"))
	if index < 0 {
		t.Fatal("release frontend has no embedded keepalive companion")
	}
	end := index
	for end < len(raw) && strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=", rune(raw[end])) {
		end++
	}
	decoded, err := base64.StdEncoding.DecodeString(string(raw[index:end]))
	if err != nil {
		t.Fatal(err)
	}
	companion, err := pe.NewFile(bytes.NewReader(decoded))
	if err != nil {
		t.Fatal(err)
	}
	defer companion.Close()
	if companion.OptionalHeader.(*pe.OptionalHeader64).Subsystem != pe.IMAGE_SUBSYSTEM_WINDOWS_GUI || companion.Machine != pe.IMAGE_FILE_MACHINE_AMD64 {
		t.Fatal("embedded companion allocates a console or targets the wrong architecture")
	}
}

type captureRunner struct {
	request buildRequest
}

func (runner *captureRunner) Build(_ context.Context, request buildRequest) error {
	runner.request = request
	return os.WriteFile(request.Output, []byte("windows-binary"), 0o755)
}

func TestBuildWindowsFrontendBindsAcceptedWSLAndCatalog(t *testing.T) {
	root := t.TempDir()
	wsl := filepath.Join(root, "loki.wsl")
	catalog := filepath.Join(root, "connect-helpers.json")
	output := filepath.Join(root, "loki.exe")
	if err := os.WriteFile(wsl, []byte("wsl"), 0o644); err != nil {
		t.Fatal(err)
	}
	asset := func(name, digest string) connect.Asset {
		return connect.Asset{
			SourceURL:    "https://example.com/releases/v1.2.3/" + name,
			SourceSHA256: digest,
			SourceLength: 1,
			MirrorAsset:  name,
		}
	}
	raw, err := connect.EncodeCatalog(connect.Catalog{
		SchemaVersion: 1,
		Helpers: []connect.Helper{{
			ID: "openai-tunnel-client", Provider: "openai", Version: "1.2.3", Platform: "windows-amd64",
			Executable: "tunnel-client.exe", ArchiveMembers: []string{"tunnel-client.exe"},
			Archive:       asset("a.zip", strings.Repeat("a", 64)),
			LicenseReport: asset("l.txt", strings.Repeat("b", 64)),
			Notice:        asset("NOTICE.txt", strings.Repeat("c", 64)),
			SPDX:          asset("s.json", strings.Repeat("d", 64)),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalog, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &captureRunner{}
	err = buildWindowsFrontend(t.Context(), options{
		Output: output, ReleaseTag: "v1.2.3", SourceRevision: strings.Repeat("e", 40),
		ReleasedAt: "2026-09-27T00:00:00Z", WSLAppliance: wsl, HelperCatalog: catalog, SourceRoot: root,
	}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if runner.request.Version != "1.2.3" || runner.request.WSL.Length != 3 ||
		runner.request.HelperCatalog.Length != int64(len(raw)) {
		t.Fatalf("unexpected build request %#v", runner.request)
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}
}

func TestReadIdentityRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readIdentity(link, 1024); err == nil {
		t.Fatal("symlink input was accepted")
	}
}
