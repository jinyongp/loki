package releases

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func materializeRepositoryFixture(t *testing.T, fixture *repositoryFixture, repo *memoryFetcher) string {
	t.Helper()
	root := t.TempDir()
	prefix := fixture.baseURL + "/"
	for sourceURL, raw := range repo.files {
		if !strings.HasPrefix(sourceURL, prefix) {
			t.Fatalf("fixture URL %q is outside repository base", sourceURL)
		}
		relative := strings.TrimPrefix(sourceURL, prefix)
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "1.root.json"), fixture.bootstrapRoot, 0644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestVerifyTUFRepositoryDirectoryUsesProductionClientPath(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	repo := fixture.repository(t, 1, now.Add(24*time.Hour))
	root := materializeRepositoryFixture(t, fixture, repo)

	releaseRaw := append([]byte("release-manifest"), byte(10))
	releaseInfo := targetInfo(t, "releases/loki-1.2.3.json", releaseRaw)
	releaseDescriptor, err := descriptorFromTargetInfo(releaseInfo.Path, releaseInfo)
	if err != nil {
		t.Fatal(err)
	}
	toolchainRaw := append([]byte("toolchain-catalog"), byte(10))
	toolchainInfo := targetInfo(t, "toolchains/catalog-v7.json", toolchainRaw)
	toolchainDescriptor, err := descriptorFromTargetInfo(toolchainInfo.Path, toolchainInfo)
	if err != nil {
		t.Fatal(err)
	}

	err = VerifyTUFRepositoryDirectory(t.Context(), root, fixture.bootstrapRoot, []RepositoryRequirement{
		{Descriptor: releaseDescriptor},
		{Descriptor: toolchainDescriptor},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifyTUFRepositoryDirectoryRejectsTamperingAndRootDrift(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	repo := fixture.repository(t, 1, now.Add(24*time.Hour))
	root := materializeRepositoryFixture(t, fixture, repo)

	releaseRaw := append([]byte("release-manifest"), byte(10))
	releaseInfo := targetInfo(t, "releases/loki-1.2.3.json", releaseRaw)
	releaseDescriptor, err := descriptorFromTargetInfo(releaseInfo.Path, releaseInfo)
	if err != nil {
		t.Fatal(err)
	}
	hash := releaseInfo.Hashes["sha256"].String()
	targetPath := filepath.Join(root, "targets", "releases", hash+".loki-1.2.3.json")
	if err = os.WriteFile(targetPath, append([]byte("tampered-release!"), byte(10)), 0644); err != nil {
		t.Fatal(err)
	}
	if err = VerifyTUFRepositoryDirectory(t.Context(), root, fixture.bootstrapRoot, []RepositoryRequirement{
		{Descriptor: releaseDescriptor},
	}); err == nil {
		t.Fatal("tampered consistent target was accepted")
	}

	root = materializeRepositoryFixture(t, fixture, repo)
	publishedRoot := filepath.Join(root, "1.root.json")
	if err = os.WriteFile(publishedRoot, append(append([]byte(nil), fixture.bootstrapRoot...), byte(10)), 0644); err != nil {
		t.Fatal(err)
	}
	if err = VerifyTUFRepositoryDirectory(t.Context(), root, fixture.bootstrapRoot, nil); err == nil {
		t.Fatal("published root bytes that differ from bootstrap trust were accepted")
	}
}

func TestVerifyTUFRepositoryDirectoryRejectsNestedSymlinkEscape(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	repo := fixture.repository(t, 1, now.Add(24*time.Hour))
	root := materializeRepositoryFixture(t, fixture, repo)

	releaseRaw := append([]byte("release-manifest"), byte(10))
	releaseInfo := targetInfo(t, "releases/loki-1.2.3.json", releaseRaw)
	releaseDescriptor, err := descriptorFromTargetInfo(releaseInfo.Path, releaseInfo)
	if err != nil {
		t.Fatal(err)
	}

	releasesDir := filepath.Join(root, "targets", "releases")
	external := t.TempDir()
	entries, err := os.ReadDir(releasesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		raw, readErr := os.ReadFile(filepath.Join(releasesDir, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(external, entry.Name()), raw, 0644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err = os.RemoveAll(releasesDir); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(external, releasesDir); err != nil {
		t.Fatal(err)
	}

	if err = VerifyTUFRepositoryDirectory(t.Context(), root, fixture.bootstrapRoot, []RepositoryRequirement{
		{Descriptor: releaseDescriptor},
	}); err == nil {
		t.Fatal("nested repository symlink escape was accepted")
	}
}

func TestBuildAndVerifyTUFRepositoryArchiveIsDeterministic(t *testing.T) {
	now := time.Now().UTC()
	fixture := newRepositoryFixture(t, now)
	repo := fixture.repository(t, 1, now.Add(24*time.Hour))
	root := materializeRepositoryFixture(t, fixture, repo)

	releaseRaw := append([]byte("release-manifest"), byte(10))
	releaseInfo := targetInfo(t, "releases/loki-1.2.3.json", releaseRaw)
	releaseDescriptor, err := descriptorFromTargetInfo(releaseInfo.Path, releaseInfo)
	if err != nil {
		t.Fatal(err)
	}
	toolchainRaw := append([]byte("toolchain-catalog"), byte(10))
	toolchainInfo := targetInfo(t, "toolchains/catalog-v7.json", toolchainRaw)
	toolchainDescriptor, err := descriptorFromTargetInfo(toolchainInfo.Path, toolchainInfo)
	if err != nil {
		t.Fatal(err)
	}
	requirements := []RepositoryRequirement{
		{Descriptor: releaseDescriptor},
		{Descriptor: toolchainDescriptor},
	}

	first := filepath.Join(t.TempDir(), "first.tar.gz")
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err = BuildTUFRepositoryArchive(t.Context(), root, first, fixture.bootstrapRoot, requirements); err != nil {
		t.Fatal(err)
	}
	if err = BuildTUFRepositoryArchive(t.Context(), root, second, fixture.bootstrapRoot, requirements); err != nil {
		t.Fatal(err)
	}
	firstRaw, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstRaw, secondRaw) {
		t.Fatal("deterministic TUF repository archive bytes differ")
	}
	if err = VerifyTUFRepositoryArchive(t.Context(), first, fixture.bootstrapRoot, requirements); err != nil {
		t.Fatal(err)
	}

	extracted := t.TempDir()
	if err = ExtractTUFRepositoryArchive(first, extracted); err != nil {
		t.Fatal(err)
	}
	if err = VerifyTUFRepositoryDirectory(t.Context(), extracted, fixture.bootstrapRoot, requirements); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(fixture.bootstrapRoot)
	trusted, err := TrustedTUFRootForDigest(extracted, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(trusted, fixture.bootstrapRoot) {
		t.Fatal("trusted root lookup returned different bytes")
	}
}

func TestExtractTUFRepositoryArchiveRejectsUnsafeEntries(t *testing.T) {
	for name, header := range map[string]*tar.Header{
		"traversal": {Name: "../escape", Mode: 0644, Size: 1, Typeflag: tar.TypeReg},
		"absolute":  {Name: "/escape", Mode: 0644, Size: 1, Typeflag: tar.TypeReg},
		"symlink":   {Name: "link", Mode: 0777, Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
	} {
		t.Run(name, func(t *testing.T) {
			var buffer bytes.Buffer
			gzipWriter := gzip.NewWriter(&buffer)
			tarWriter := tar.NewWriter(gzipWriter)
			if err := tarWriter.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if header.Typeflag == tar.TypeReg {
				if _, err := tarWriter.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			}
			if err := tarWriter.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gzipWriter.Close(); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "bad.tar.gz")
			if err := os.WriteFile(archive, buffer.Bytes(), 0644); err != nil {
				t.Fatal(err)
			}
			if err := ExtractTUFRepositoryArchive(archive, t.TempDir()); err == nil {
				t.Fatal("unsafe TUF repository archive was accepted")
			}
		})
	}
}
