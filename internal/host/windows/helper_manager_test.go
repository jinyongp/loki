package windows

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"loki/internal/host/connect"
)

type fakeHelperDownloader struct {
	files map[string][]byte
	calls []string
}

func (downloader *fakeHelperDownloader) Fetch(_ context.Context, url string, _ int64) ([]byte, error) {
	downloader.calls = append(downloader.calls, url)
	raw, ok := downloader.files[url]
	if !ok {
		return nil, fmt.Errorf("unexpected download %s", url)
	}
	return append([]byte(nil), raw...), nil
}

type fakeHelperPlatform struct {
	files      map[string][]byte
	dirs       map[string]bool
	private    map[string]bool
	reparse    map[string]bool
	tempCount  int
	publishErr error
}

func newFakeHelperPlatform() *fakeHelperPlatform {
	return &fakeHelperPlatform{
		files: map[string][]byte{}, dirs: map[string]bool{},
		private: map[string]bool{}, reparse: map[string]bool{},
	}
}

func (platform *fakeHelperPlatform) Lstat(path string) (StatePath, error) {
	if platform.dirs[path] {
		return StatePath{Exists: true, Directory: true, Reparse: platform.reparse[path]}, nil
	}
	if _, ok := platform.files[path]; ok {
		return StatePath{Exists: true, Regular: true, Reparse: platform.reparse[path]}, nil
	}
	return StatePath{}, nil
}

func (platform *fakeHelperPlatform) ReadFile(path string) ([]byte, error) {
	raw, ok := platform.files[path]
	if !ok {
		return nil, errors.New("missing file")
	}
	return append([]byte(nil), raw...), nil
}

func (platform *fakeHelperPlatform) FileDigest(path string) (string, int64, error) {
	raw, ok := platform.files[path]
	if !ok {
		return "", 0, errors.New("missing file")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), int64(len(raw)), nil
}

func (platform *fakeHelperPlatform) ListDirectory(root string) ([]string, error) {
	if !platform.dirs[root] {
		return nil, errors.New("missing directory")
	}
	prefix := root + "\\"
	var names []string
	seen := map[string]bool{}
	for name := range platform.files {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if rest != "" && !strings.Contains(rest, "\\") && !seen[rest] {
			names = append(names, rest)
			seen[rest] = true
		}
	}
	for name := range platform.dirs {
		if name == root || !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if rest != "" && !strings.Contains(rest, "\\") && !seen[rest] {
			names = append(names, rest)
			seen[rest] = true
		}
	}
	slices.Sort(names)
	return names, nil
}

func (platform *fakeHelperPlatform) EnsurePrivateDirectory(_ context.Context, path string) error {
	if _, file := platform.files[path]; file || platform.reparse[path] {
		return errors.New("unsafe directory")
	}
	platform.dirs[path] = true
	platform.private[path] = true
	return nil
}

func (platform *fakeHelperPlatform) VerifyPrivatePath(path string, directory bool) error {
	if !platform.private[path] {
		return errors.New("ACL drift")
	}
	if directory && !platform.dirs[path] {
		return errors.New("not directory")
	}
	if !directory {
		if _, ok := platform.files[path]; !ok {
			return errors.New("not file")
		}
	}
	if platform.reparse[path] {
		return errors.New("reparse")
	}
	return nil
}

func (platform *fakeHelperPlatform) CreatePrivateTempDirectory(_ context.Context, parent, prefix string) (string, error) {
	platform.tempCount++
	path := parent + "\\" + prefix + fmt.Sprint(platform.tempCount)
	platform.dirs[path] = true
	platform.private[path] = true
	return path, nil
}

func (platform *fakeHelperPlatform) WritePrivateFile(_ context.Context, path string, raw []byte) error {
	if _, exists := platform.files[path]; exists {
		return errors.New("file exists")
	}
	platform.files[path] = append([]byte(nil), raw...)
	platform.private[path] = true
	return nil
}

func (platform *fakeHelperPlatform) PublishDirectory(staging, target string) error {
	if platform.publishErr != nil {
		return platform.publishErr
	}
	if platform.dirs[target] {
		return errors.New("target exists")
	}
	if !platform.dirs[staging] {
		return errors.New("missing staging")
	}
	platform.dirs[target] = true
	platform.private[target] = platform.private[staging]
	prefix := staging + "\\"
	for name, raw := range platform.files {
		if strings.HasPrefix(name, prefix) {
			next := target + "\\" + strings.TrimPrefix(name, prefix)
			platform.files[next] = append([]byte(nil), raw...)
			platform.private[next] = platform.private[name]
		}
	}
	for name := range platform.dirs {
		if name != staging && strings.HasPrefix(name, prefix) {
			next := target + "\\" + strings.TrimPrefix(name, prefix)
			platform.dirs[next] = true
			platform.private[next] = platform.private[name]
		}
	}
	_ = platform.RemoveTree(staging)
	return nil
}

func (platform *fakeHelperPlatform) RemoveTree(root string) error {
	delete(platform.dirs, root)
	delete(platform.private, root)
	delete(platform.reparse, root)
	prefix := root + "\\"
	for name := range platform.files {
		if name == root || strings.HasPrefix(name, prefix) {
			delete(platform.files, name)
			delete(platform.private, name)
			delete(platform.reparse, name)
		}
	}
	for name := range platform.dirs {
		if strings.HasPrefix(name, prefix) {
			delete(platform.dirs, name)
			delete(platform.private, name)
			delete(platform.reparse, name)
		}
	}
	return nil
}

func helperZip(t *testing.T, files map[string][]byte, symlink string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if name == symlink {
			header.SetMode(0o777 | os.ModeSymlink)
		} else {
			header.SetMode(0o644)
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func helperManagerFixture(t *testing.T) (HelperManager, *fakeHelperPlatform, *fakeHelperDownloader, connect.Helper) {
	t.Helper()
	archiveFiles := map[string][]byte{
		"NOTICE":            []byte("notice"),
		"tunnel-client.exe": []byte("exe-bytes"),
	}
	archive := helperZip(t, archiveFiles, "")
	sum := sha256.Sum256(archive)
	asset := func(source, mirror string, length int64, digest string) connect.Asset {
		return connect.Asset{
			SourceURL:    "https://provider.example/releases/v1.2.3/" + source,
			SourceSHA256: digest, SourceLength: length, MirrorAsset: mirror,
		}
	}
	helper := connect.Helper{
		ID: "openai-tunnel-client", Provider: "openai", Version: "1.2.3",
		Platform: "windows-amd64", Executable: "tunnel-client.exe",
		ArchiveMembers: []string{"NOTICE", "tunnel-client.exe"},
		Archive:        asset("client.zip", "loki-helper-openai.zip", int64(len(archive)), hex.EncodeToString(sum[:])),
		LicenseReport:  asset("licenses.txt", "loki-helper-openai-licenses.txt", 1, strings.Repeat("1", 64)),
		Notice:         asset("NOTICE", "loki-helper-openai-NOTICE.txt", 1, strings.Repeat("2", 64)),
		SPDX:           asset("spdx.json", "loki-helper-openai.spdx.json", 1, strings.Repeat("3", 64)),
	}
	catalogRaw, err := connect.EncodeCatalog(connect.Catalog{SchemaVersion: 1, Helpers: []connect.Helper{helper}})
	if err != nil {
		t.Fatal(err)
	}
	catalogSum := sha256.Sum256(catalogRaw)
	binding := ReleaseBinding{
		ReleaseTag:    "v1.2.3",
		HelperCatalog: FileBinding{SHA256: hex.EncodeToString(catalogSum[:]), Length: int64(len(catalogRaw))},
	}
	catalogURL, _ := helperMirrorURL(binding.ReleaseTag, helperCatalogAssetName)
	archiveURL, _ := helperMirrorURL(binding.ReleaseTag, helper.Archive.MirrorAsset)
	downloader := &fakeHelperDownloader{files: map[string][]byte{
		catalogURL: catalogRaw,
		archiveURL: archive,
	}}
	platform := newFakeHelperPlatform()
	manager := HelperManager{
		Platform: platform, Downloader: downloader, Binding: binding,
		HelpersRoot: `C:\Users\alice\AppData\Local\Programs\Loki\helpers`,
	}
	return manager, platform, downloader, helper
}

func TestHelperManagerInstallsFromSameReleaseMirrorAndReusesByDigest(t *testing.T) {
	manager, platform, downloader, helper := helperManagerFixture(t)
	first, err := manager.Ensure(t.Context(), helper.ID, helper.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || !strings.HasSuffix(first.ExecutablePath, `\tunnel-client.exe`) {
		t.Fatalf("first=%+v", first)
	}
	if len(downloader.calls) != 2 {
		t.Fatalf("downloads=%v", downloader.calls)
	}
	for _, call := range downloader.calls {
		if strings.Contains(call, "provider.example") || !strings.Contains(call, "/releases/download/v1.2.3/") {
			t.Fatalf("runtime contacted non-release mirror URL %q", call)
		}
	}

	second, err := manager.Ensure(t.Context(), helper.ID, helper.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused {
		t.Fatalf("second=%+v", second)
	}
	if len(downloader.calls) != 3 {
		t.Fatalf("reuse should revalidate catalog but not redownload archive: %v", downloader.calls)
	}

	platform.files[second.ExecutablePath] = []byte("corrupt")
	if _, err = manager.Ensure(t.Context(), helper.ID, helper.Platform); err == nil ||
		!strings.Contains(err.Error(), "no longer matches installed ownership") {
		t.Fatalf("corrupt installed helper accepted: %v", err)
	}
}

func TestHelperManagerRejectsPrivateACLDrift(t *testing.T) {
	manager, platform, _, helper := helperManagerFixture(t)
	installed, err := manager.Ensure(t.Context(), helper.ID, helper.Platform)
	if err != nil {
		t.Fatal(err)
	}
	platform.private[installed.Root] = false
	if _, err = manager.Ensure(t.Context(), helper.ID, helper.Platform); err == nil ||
		!strings.Contains(err.Error(), "ACL drift") {
		t.Fatalf("ACL drift accepted: %v", err)
	}
}

func TestHelperManagerRejectsWrongCatalogOrArchiveIdentityWithoutPublishing(t *testing.T) {
	manager, platform, downloader, helper := helperManagerFixture(t)
	catalogURL, _ := helperMirrorURL(manager.Binding.ReleaseTag, helperCatalogAssetName)
	downloader.files[catalogURL] = append(downloader.files[catalogURL], '\n')
	if _, err := manager.Ensure(t.Context(), helper.ID, helper.Platform); err == nil {
		t.Fatal("wrong catalog identity accepted")
	}
	if len(platform.dirs) != 0 || len(platform.files) != 0 {
		t.Fatalf("catalog failure mutated helper state: dirs=%v files=%v", platform.dirs, platform.files)
	}

	manager, platform, downloader, helper = helperManagerFixture(t)
	archiveURL, _ := helperMirrorURL(manager.Binding.ReleaseTag, helper.Archive.MirrorAsset)
	downloader.files[archiveURL] = append(downloader.files[archiveURL], 'x')
	if _, err := manager.Ensure(t.Context(), helper.ID, helper.Platform); err == nil {
		t.Fatal("wrong archive identity accepted")
	}
	target := joinWindowsPath(manager.HelpersRoot, helper.ID+"\\"+helper.Version+"\\"+helper.Platform)
	if platform.dirs[target] {
		t.Fatal("archive verification failure published helper target")
	}
}

func TestExtractHelperArchiveRejectsTraversalSymlinkAndUnexpectedMembers(t *testing.T) {
	_, _, _, helper := helperManagerFixture(t)
	tests := map[string][]byte{
		"traversal": helperZip(t, map[string][]byte{
			"NOTICE": []byte("notice"), "tunnel-client.exe": []byte("exe"), "../evil": []byte("evil"),
		}, ""),
		"symlink": helperZip(t, map[string][]byte{
			"NOTICE": []byte("notice"), "tunnel-client.exe": []byte("target"),
		}, "tunnel-client.exe"),
		"unexpected": helperZip(t, map[string][]byte{
			"NOTICE": []byte("notice"), "tunnel-client.exe": []byte("exe"), "extra": []byte("x"),
		}, ""),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := extractHelperArchive(raw, helper); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func TestHelperManagerPublishFailureLeavesNoTarget(t *testing.T) {
	manager, platform, _, helper := helperManagerFixture(t)
	platform.publishErr = errors.New("rename failed")
	if _, err := manager.Ensure(t.Context(), helper.ID, helper.Platform); err == nil {
		t.Fatal("publish failure ignored")
	}
	target := joinWindowsPath(manager.HelpersRoot, helper.ID+"\\"+helper.Version+"\\"+helper.Platform)
	if platform.dirs[target] {
		t.Fatal("failed publication left target")
	}
	for name := range platform.dirs {
		if strings.Contains(name, ".loki-helper-") {
			t.Fatalf("failed publication left staging directory %q", name)
		}
	}
}
