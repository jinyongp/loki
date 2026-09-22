package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"loki/internal/host/releases"
	"loki/tools/release/internal/bootstrapinfo"
)

const (
	maxInstallerTemplateBytes = int64(64 << 10)
	maxEvidenceBytes          = int64(2 << 20)
	publicTUFMetadataURL      = "https://jinyongp.dev/loki/tuf/"
)

var (
	fullCommitPattern             = regexp.MustCompile(`^[0-9a-f]{40}$`)
	inspectPublicationBootstrap   = bootstrapinfo.Inspect
	extractPublicationTUFArchive  = releases.ExtractTUFRepositoryArchive
	trustedPublicationTUFRoot     = releases.TrustedTUFRootForDigest
	verifyPublicationTUFDirectory = releases.VerifyTUFRepositoryDirectory
)

type options struct {
	Candidate         string
	Output            string
	Tag               string
	Commit            string
	InstallerTemplate string
}

type publicationAsset struct {
	Name     string
	Evidence releases.FileEvidence
	Mode     os.FileMode
}

func main() {
	var cfg options
	flag.StringVar(&cfg.Candidate, "candidate", "", "verified A14 candidate evidence bundle")
	flag.StringVar(&cfg.Output, "output", "", "absolute output directory")
	flag.StringVar(&cfg.Tag, "tag", "", "existing Git release tag")
	flag.StringVar(&cfg.Commit, "commit", "", "full source commit SHA")
	flag.StringVar(&cfg.InstallerTemplate, "installer-template", "", "install.sh template path")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if err := preparePublication(cfg); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func preparePublication(cfg options) error {
	candidate, err := cleanAbsolutePath(cfg.Candidate, "candidate bundle")
	if err != nil {
		return err
	}
	output, err := cleanAbsolutePath(cfg.Output, "publication output")
	if err != nil {
		return err
	}
	templatePath, err := cleanAbsolutePath(cfg.InstallerTemplate, "installer template")
	if err != nil {
		return err
	}
	if _, err = os.Lstat(output); err == nil {
		return errors.New("publication output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	commit := strings.ToLower(strings.TrimSpace(cfg.Commit))
	if !fullCommitPattern.MatchString(commit) {
		return errors.New("publication commit must be a full 40-character lowercase Git SHA")
	}

	evidence, err := releases.VerifyCandidateBundle(candidate)
	if err != nil {
		return fmt.Errorf("verify candidate bundle: %w", err)
	}
	if evidence.SourceRevision != commit {
		return errors.New("candidate source revision does not match publication commit")
	}
	expectedTag := "v" + evidence.Generation.Spec.Version
	if strings.TrimSpace(cfg.Tag) != expectedTag {
		return fmt.Errorf("publication tag must be %s for candidate release %s", expectedTag, evidence.Generation.Spec.Version)
	}
	bootstrapPath := filepath.Join(candidate, filepath.FromSlash(evidence.Bootstrap.Path))
	bootstrapTrust, err := inspectPublicationBootstrap(context.Background(), bootstrapPath)
	if err != nil {
		return err
	}
	if bootstrapTrust.MetadataURL != publicTUFMetadataURL {
		return fmt.Errorf("accepted bootstrap metadata URL must be %s", publicTUFMetadataURL)
	}

	manifestRaw, err := readEvidenceBytes(candidate, evidence.ReleaseManifest, 4<<20)
	if err != nil {
		return err
	}
	manifest, err := releases.LoadReleaseManifest(manifestRaw)
	if err != nil {
		return fmt.Errorf("load candidate release manifest: %w", err)
	}
	if manifest.Generation.ID != evidence.Generation.ID {
		return errors.New("candidate release manifest generation does not match evidence")
	}
	indexRaw, err := readEvidenceBytes(candidate, evidence.ReleaseIndex, 4<<20)
	if err != nil {
		return err
	}
	index, err := releases.LoadReleaseIndex(indexRaw)
	if err != nil {
		return fmt.Errorf("load candidate release index: %w", err)
	}
	indexEntry, err := verifyIndexManifestBinding(index, manifest, manifestRaw)
	if err != nil {
		return err
	}
	repositoryRequirements := []releases.RepositoryRequirement{
		{Descriptor: releases.TargetDescriptor{
			Path: "releases/index.json", Length: evidence.ReleaseIndex.Length, SHA256: evidence.ReleaseIndex.SHA256,
		}},
		{Descriptor: indexEntry.Manifest},
		{Descriptor: manifest.HostBinary},
		{Descriptor: manifest.Bootstrap},
		{Descriptor: manifest.HostAssets},
		{Descriptor: manifest.ToolchainCatalog},
		{Descriptor: manifest.Provenance},
		{Descriptor: manifest.Notices},
		{Descriptor: manifest.ReleaseNotes},
	}

	template, err := readRegularBounded(templatePath, maxInstallerTemplateBytes)
	if err != nil {
		return fmt.Errorf("read installer template: %w", err)
	}
	installer, err := renderInstaller(template, expectedTag, evidence.Bootstrap.SHA256)
	if err != nil {
		return err
	}

	parent := filepath.Dir(output)
	if err = ensureRealDirectory(parent); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, ".loki-publication-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(temp)
		}
	}()
	if err = os.Chmod(temp, 0755); err != nil {
		return err
	}
	assetsDir := filepath.Join(temp, "assets")
	pagesDir := filepath.Join(temp, "pages")
	tufDir := filepath.Join(pagesDir, "tuf")
	for _, dir := range []string{assetsDir, pagesDir, tufDir} {
		if err = os.Mkdir(dir, 0755); err != nil {
			return err
		}
		if err = os.Chmod(dir, 0755); err != nil {
			return err
		}
	}

	assets := []publicationAsset{
		{Name: "loki-linux-amd64", Evidence: evidence.HostBinary, Mode: 0755},
		{Name: "loki-bootstrap-linux-amd64", Evidence: evidence.Bootstrap, Mode: 0755},
		{Name: "loki-tuf-repository.tar.gz", Evidence: evidence.TUFRepository, Mode: 0644},
		{Name: "loki-host-assets.tar.gz", Evidence: evidence.HostAssets, Mode: 0644},
		{Name: "loki-release-index.json", Evidence: evidence.ReleaseIndex, Mode: 0644},
		{Name: "loki-release-manifest.json", Evidence: evidence.ReleaseManifest, Mode: 0644},
		{Name: "loki-toolchain-catalog.json", Evidence: evidence.ToolchainCatalog, Mode: 0644},
		{Name: "loki-provenance.bundle.json", Evidence: evidence.Provenance, Mode: 0644},
		{Name: "loki-notices.tar.gz", Evidence: evidence.Notices, Mode: 0644},
		{Name: "loki-release-notes.md", Evidence: evidence.ReleaseNotes, Mode: 0644},
	}
	for _, asset := range assets {
		if err = copyEvidenceAsset(candidate, assetsDir, asset); err != nil {
			return err
		}
	}

	tufArchivePath := filepath.Join(candidate, filepath.FromSlash(evidence.TUFRepository.Path))
	if err = extractPublicationTUFArchive(tufArchivePath, tufDir); err != nil {
		return fmt.Errorf("extract accepted TUF repository: %w", err)
	}
	trustedRoot, err := trustedPublicationTUFRoot(tufDir, bootstrapTrust.TrustedRootSHA256)
	if err != nil {
		return fmt.Errorf("resolve accepted TUF trusted root: %w", err)
	}
	if err = verifyPublicationTUFDirectory(
		context.Background(), tufDir, trustedRoot, repositoryRequirements,
	); err != nil {
		return fmt.Errorf("verify accepted TUF repository: %w", err)
	}

	evidenceRaw, err := readRegularBounded(filepath.Join(candidate, "evidence.json"), maxEvidenceBytes)
	if err != nil {
		return fmt.Errorf("read candidate evidence: %w", err)
	}
	if _, err = releases.LoadCandidateEvidence(evidenceRaw); err != nil {
		return fmt.Errorf("load candidate evidence: %w", err)
	}
	if err = writeSyncedFile(filepath.Join(assetsDir, "loki-candidate-evidence.json"), evidenceRaw, 0644); err != nil {
		return err
	}
	if err = writeSyncedFile(filepath.Join(assetsDir, "loki-install.sh"), installer, 0755); err != nil {
		return err
	}
	if err = writeSyncedFile(filepath.Join(pagesDir, "install.sh"), installer, 0644); err != nil {
		return err
	}
	if err = writeAssetChecksums(assetsDir); err != nil {
		return err
	}
	for _, dir := range []string{assetsDir, tufDir, pagesDir, temp} {
		if err = syncDirectory(dir); err != nil {
			return err
		}
	}
	if err = unix.Renameat2(unix.AT_FDCWD, temp, unix.AT_FDCWD, output, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errors.New("publication output already exists")
		}
		return err
	}
	cleanup = false
	return syncDirectory(parent)
}

func verifyIndexManifestBinding(index releases.ReleaseIndex, manifest releases.ReleaseManifest, raw []byte) (releases.ReleaseIndexEntry, error) {
	for _, entry := range index.Entries {
		if entry.Release != manifest.Generation.Spec.Version || entry.GenerationID != manifest.Generation.ID {
			continue
		}
		verified, err := entry.VerifyManifest(raw)
		if err != nil {
			return releases.ReleaseIndexEntry{}, fmt.Errorf("verify release index manifest binding: %w", err)
		}
		if verified.Generation.ID != manifest.Generation.ID {
			return releases.ReleaseIndexEntry{}, errors.New("release index manifest binding changed generation")
		}
		return entry, nil
	}
	return releases.ReleaseIndexEntry{}, errors.New("release index does not contain the candidate release manifest")
}

func renderInstaller(template []byte, tag, bootstrapSHA256 string) ([]byte, error) {
	const tagPlaceholder = "@@LOKI_RELEASE_TAG@@"
	const digestPlaceholder = "@@LOKI_BOOTSTRAP_SHA256@@"
	text := string(template)
	if strings.Count(text, tagPlaceholder) != 1 || strings.Count(text, digestPlaceholder) != 1 {
		return nil, errors.New("installer template must contain each release placeholder exactly once")
	}
	if !fullCommitSafeToken(tag) || len(bootstrapSHA256) != sha256.Size*2 {
		return nil, errors.New("installer release identity is invalid")
	}
	if _, err := hex.DecodeString(bootstrapSHA256); err != nil {
		return nil, errors.New("installer bootstrap digest is invalid")
	}
	text = strings.Replace(text, tagPlaceholder, tag, 1)
	text = strings.Replace(text, digestPlaceholder, bootstrapSHA256, 1)
	if strings.Contains(text, "@@LOKI_") {
		return nil, errors.New("installer template contains unresolved Loki placeholders")
	}
	return []byte(text), nil
}

func fullCommitSafeToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("._-", r):
		default:
			return false
		}
	}
	return true
}

func copyEvidenceAsset(candidate, destinationRoot string, asset publicationAsset) error {
	source := filepath.Join(candidate, filepath.FromSlash(asset.Evidence.Path))
	input, info, err := openRegularNoFollow(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if info.Size() != asset.Evidence.Length {
		return fmt.Errorf("candidate input %s changed length before publication", asset.Evidence.Path)
	}
	destination := filepath.Join(destinationRoot, asset.Name)
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, asset.Mode)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != asset.Evidence.Length || hex.EncodeToString(hash.Sum(nil)) != asset.Evidence.SHA256 {
		return fmt.Errorf("candidate input %s changed before publication", asset.Evidence.Path)
	}
	return nil
}

func readEvidenceBytes(candidate string, evidence releases.FileEvidence, maximum int64) ([]byte, error) {
	if evidence.Length <= 0 || evidence.Length > maximum {
		return nil, errors.New("candidate publication input exceeds size policy")
	}
	path := filepath.Join(candidate, filepath.FromSlash(evidence.Path))
	raw, err := readRegularBounded(path, maximum)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if int64(len(raw)) != evidence.Length || hex.EncodeToString(sum[:]) != evidence.SHA256 {
		return nil, errors.New("candidate publication input does not match evidence")
	}
	return raw, nil
}

func writeAssetChecksums(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == "SHA256SUMS" {
			continue
		}
		path := filepath.Join(root, entry.Name())
		file, info, openErr := openRegularNoFollow(path)
		if openErr != nil {
			return openErr
		}
		hash := sha256.New()
		_, hashErr := io.Copy(hash, file)
		closeErr := file.Close()
		if hashErr != nil {
			return hashErr
		}
		if closeErr != nil {
			return closeErr
		}
		if info.Size() <= 0 {
			return fmt.Errorf("publication asset %s is empty", entry.Name())
		}
		lines = append(lines, fmt.Sprintf("%s  %s", hex.EncodeToString(hash.Sum(nil)), entry.Name()))
	}
	sort.Strings(lines)
	return writeSyncedFile(filepath.Join(root, "SHA256SUMS"), []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func writeSyncedFile(path string, raw []byte, mode os.FileMode) error {
	if len(raw) == 0 {
		return errors.New("publication file is empty")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readRegularBounded(path string, maximum int64) ([]byte, error) {
	file, info, err := openRegularNoFollow(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("publication input exceeds size policy")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != info.Size() {
		return nil, errors.New("publication input changed while being read")
	}
	return raw, nil
}

func openRegularNoFollow(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("publication input must be a regular file")
	}
	return file, info, nil
}

func cleanAbsolutePath(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value ||
		value == string(filepath.Separator) || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s must be a clean absolute non-root path", name)
	}
	return value, nil
}

func ensureRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("publication output parent must be a real directory")
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
