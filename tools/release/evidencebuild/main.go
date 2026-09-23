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
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"loki/internal/host/releases"
	"loki/tools/release/internal/bootstrapinfo"
)

const (
	maxMetadataBytes      = int64(16 << 20)
	maxReleaseTargetBytes = int64(1 << 30)
)

var inspectBootstrapRelease = bootstrapinfo.Inspect

type options struct {
	Output           string
	SourceRevision   string
	ReleaseIndex     string
	ReleaseManifest  string
	HostBinary       string
	Bootstrap        string
	HostAssets       string
	ToolchainCatalog string
	Provenance       string
	Notices          string
	ReleaseNotes     string
	EffectivePolicy  string
	EffectiveConfig  string
	CoreImage        string
	BrowserImage     string
}

func main() {
	if err := run(os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("evidencebuild", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfg options
	flags.StringVar(&cfg.Output, "output", "", "absolute output directory")
	flags.StringVar(&cfg.SourceRevision, "source-revision", "", "immutable source revision")
	flags.StringVar(&cfg.ReleaseIndex, "release-index", "", "release index JSON")
	flags.StringVar(&cfg.ReleaseManifest, "release-manifest", "", "release manifest JSON")
	flags.StringVar(&cfg.HostBinary, "host-binary", "", "Loki host binary")
	flags.StringVar(&cfg.Bootstrap, "bootstrap", "", "standalone bootstrap binary")
	flags.StringVar(&cfg.HostAssets, "host-assets", "", "host asset bundle")
	flags.StringVar(&cfg.ToolchainCatalog, "toolchain-catalog", "", "managed toolchain catalog")
	flags.StringVar(&cfg.Provenance, "provenance", "", "release provenance bundle")
	flags.StringVar(&cfg.Notices, "notices", "", "third-party notice bundle")
	flags.StringVar(&cfg.ReleaseNotes, "release-notes", "", "release notes")
	flags.StringVar(&cfg.EffectivePolicy, "effective-policy", "", "effective release policy")
	flags.StringVar(&cfg.EffectiveConfig, "effective-config", "", "effective release configuration")
	flags.StringVar(&cfg.CoreImage, "core-image", "", "immutable core OCI image reference")
	flags.StringVar(&cfg.BrowserImage, "browser-image", "", "immutable browser OCI image reference")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("evidencebuild does not accept positional arguments")
	}
	return assemble(cfg)
}

func assemble(cfg options) error {
	output, err := cleanAbsolute(cfg.Output, "output")
	if err != nil {
		return err
	}
	inputs := map[string]*string{
		"release index":     &cfg.ReleaseIndex,
		"release manifest":  &cfg.ReleaseManifest,
		"host binary":       &cfg.HostBinary,
		"bootstrap":         &cfg.Bootstrap,
		"host assets":       &cfg.HostAssets,
		"toolchain catalog": &cfg.ToolchainCatalog,
		"provenance":        &cfg.Provenance,
		"notices":           &cfg.Notices,
		"release notes":     &cfg.ReleaseNotes,
		"effective policy":  &cfg.EffectivePolicy,
		"effective config":  &cfg.EffectiveConfig,
	}
	for name, value := range inputs {
		*value, err = cleanAbsolute(*value, name)
		if err != nil {
			return err
		}
	}
	if info, statErr := os.Lstat(output); statErr == nil {
		return fmt.Errorf("output already exists: %s (%s)", output, info.Mode())
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	parent := filepath.Dir(output)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("output parent must be a real directory")
	}

	indexRaw, err := readRegular(cfg.ReleaseIndex, maxMetadataBytes)
	if err != nil {
		return fmt.Errorf("read release index: %w", err)
	}
	index, err := releases.LoadReleaseIndex(indexRaw)
	if err != nil {
		return err
	}
	manifestRaw, err := readRegular(cfg.ReleaseManifest, maxMetadataBytes)
	if err != nil {
		return fmt.Errorf("read release manifest: %w", err)
	}
	manifest, err := releases.LoadReleaseManifest(manifestRaw)
	if err != nil {
		return err
	}
	entry, err := matchingIndexEntry(index, manifest)
	if err != nil {
		return err
	}
	if _, err = entry.VerifyManifest(manifestRaw); err != nil {
		return err
	}
	bootstrapRelease, err := inspectBootstrapRelease(context.Background(), cfg.Bootstrap)
	if err != nil {
		return err
	}
	manifestSum := sha256.Sum256(manifestRaw)
	if bootstrapRelease.ReleaseTag != "v"+manifest.Generation.Spec.Version ||
		bootstrapRelease.ReleaseManifestSHA256 != hex.EncodeToString(manifestSum[:]) ||
		bootstrapRelease.HostBinarySHA256 != manifest.HostBinary.SHA256 {
		return errors.New("bootstrap release binding does not match the candidate manifest")
	}
	temp, err := os.MkdirTemp(parent, "."+filepath.Base(output)+".")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err = os.Chmod(temp, 0755); err != nil {
		return err
	}
	inputsDirectory := filepath.Join(temp, "inputs")
	if err = os.Mkdir(inputsDirectory, 0755); err != nil {
		return err
	}
	if err = os.Chmod(inputsDirectory, 0755); err != nil {
		return err
	}

	releaseIndexEvidence, err := copyEvidence(cfg.ReleaseIndex, temp, "inputs/release-index.json", nil, 0644)
	if err != nil {
		return err
	}
	releaseManifestEvidence, err := copyEvidence(cfg.ReleaseManifest, temp, "inputs/release-manifest.json", &entry.Manifest, 0644)
	if err != nil {
		return err
	}
	hostBinaryEvidence, err := copyEvidence(cfg.HostBinary, temp, "inputs/loki", &manifest.HostBinary, 0755)
	if err != nil {
		return err
	}
	bootstrapEvidence, err := copyEvidence(cfg.Bootstrap, temp, "inputs/loki-bootstrap", nil, 0755)
	if err != nil {
		return err
	}
	hostAssetsEvidence, err := copyEvidence(cfg.HostAssets, temp, "inputs/host-assets.tar.gz", &manifest.HostAssets, 0644)
	if err != nil {
		return err
	}
	toolchainEvidence, err := copyEvidence(cfg.ToolchainCatalog, temp, "inputs/toolchain-catalog.json", &manifest.ToolchainCatalog, 0644)
	if err != nil {
		return err
	}
	provenanceEvidence, err := copyEvidence(cfg.Provenance, temp, "inputs/provenance.bundle.json", &manifest.Provenance, 0644)
	if err != nil {
		return err
	}
	noticesEvidence, err := copyEvidence(cfg.Notices, temp, "inputs/notices.tar.gz", &manifest.Notices, 0644)
	if err != nil {
		return err
	}
	releaseNotesEvidence, err := copyEvidence(cfg.ReleaseNotes, temp, "inputs/release-notes.md", &manifest.ReleaseNotes, 0644)
	if err != nil {
		return err
	}
	policyEvidence, err := copyEvidence(cfg.EffectivePolicy, temp, "inputs/effective-policy.json", nil, 0644)
	if err != nil {
		return err
	}
	configEvidence, err := copyEvidence(cfg.EffectiveConfig, temp, "inputs/effective-config.toml", nil, 0644)
	if err != nil {
		return err
	}

	evidence, err := releases.NewCandidateEvidence(releases.CandidateEvidenceInput{
		SourceRevision: cfg.SourceRevision, Manifest: manifest, IndexEntry: entry,
		CoreImage: cfg.CoreImage, BrowserImage: cfg.BrowserImage,
		ReleaseIndex: releaseIndexEvidence, ReleaseManifest: releaseManifestEvidence,
		HostBinary: hostBinaryEvidence, Bootstrap: bootstrapEvidence, HostAssets: hostAssetsEvidence,
		ToolchainCatalog: toolchainEvidence, Provenance: provenanceEvidence, Notices: noticesEvidence,
		ReleaseNotes: releaseNotesEvidence, EffectivePolicy: policyEvidence, EffectiveConfig: configEvidence,
	})
	if err != nil {
		return err
	}
	evidenceRaw, err := releases.EncodeCandidateEvidence(evidence)
	if err != nil {
		return err
	}
	evidenceRaw = append(evidenceRaw, '\n')
	if err = writeEvidenceFile(filepath.Join(temp, "evidence.json"), evidenceRaw, 0644); err != nil {
		return err
	}

	files := []releases.FileEvidence{
		releaseIndexEvidence, releaseManifestEvidence, hostBinaryEvidence, bootstrapEvidence, hostAssetsEvidence,
		toolchainEvidence, provenanceEvidence, noticesEvidence, releaseNotesEvidence, policyEvidence, configEvidence,
	}
	checksums := make([]string, 0, len(files)+1)
	for _, file := range files {
		checksums = append(checksums, fmt.Sprintf("%s  %s", file.SHA256, file.Path))
	}
	evidenceSum := sha256.Sum256(evidenceRaw)
	checksums = append(checksums, fmt.Sprintf("%s  evidence.json", hex.EncodeToString(evidenceSum[:])))
	sort.Strings(checksums)
	if err = writeEvidenceFile(filepath.Join(temp, "SHA256SUMS"), []byte(strings.Join(checksums, "\n")+"\n"), 0644); err != nil {
		return err
	}
	if err = syncDirectory(filepath.Join(temp, "inputs")); err != nil {
		return err
	}
	if err = syncDirectory(temp); err != nil {
		return err
	}
	if _, err = releases.VerifyCandidateBundle(temp); err != nil {
		return fmt.Errorf("verify assembled release candidate evidence: %w", err)
	}
	if err = publishEvidenceBundle(temp, output); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func matchingIndexEntry(index releases.ReleaseIndex, manifest releases.ReleaseManifest) (releases.ReleaseIndexEntry, error) {
	for _, entry := range index.Entries {
		if entry.Release == manifest.Generation.Spec.Version && entry.GenerationID == manifest.Generation.ID {
			return entry, nil
		}
	}
	return releases.ReleaseIndexEntry{}, errors.New("release index does not contain the candidate manifest")
}

func copyEvidence(source, root, relative string, target *releases.TargetDescriptor, mode os.FileMode) (releases.FileEvidence, error) {
	sourceFile, info, err := openRegular(source, maxReleaseTargetBytes)
	if err != nil {
		return releases.FileEvidence{}, err
	}
	defer sourceFile.Close()
	destination := filepath.Join(root, filepath.FromSlash(relative))
	if err = os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
		return releases.FileEvidence{}, err
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return releases.FileEvidence{}, err
	}
	if err = out.Chmod(mode); err != nil {
		out.Close()
		return releases.FileEvidence{}, err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, digest), sourceFile)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return releases.FileEvidence{}, copyErr
	}
	if syncErr != nil {
		return releases.FileEvidence{}, syncErr
	}
	if closeErr != nil {
		return releases.FileEvidence{}, closeErr
	}
	if written != info.Size() {
		return releases.FileEvidence{}, errors.New("release input changed while being copied")
	}
	evidence := releases.FileEvidence{
		Path: relative, Length: written, SHA256: hex.EncodeToString(digest.Sum(nil)),
	}
	if target != nil && (target.Length != evidence.Length || target.SHA256 != evidence.SHA256) {
		return releases.FileEvidence{}, errors.New("release input does not match manifest target identity")
	}
	return evidence, nil
}

func descriptorFromBytes(path string, raw []byte) releases.TargetDescriptor {
	sum := sha256.Sum256(raw)
	return releases.TargetDescriptor{
		Path: path, Length: int64(len(raw)), SHA256: hex.EncodeToString(sum[:]),
	}
}

func evidenceForExisting(path, relative string) (releases.FileEvidence, error) {
	file, info, err := openRegular(path, maxReleaseTargetBytes)
	if err != nil {
		return releases.FileEvidence{}, err
	}
	defer file.Close()
	digest := sha256.New()
	written, err := io.Copy(digest, file)
	if err != nil {
		return releases.FileEvidence{}, err
	}
	if written != info.Size() {
		return releases.FileEvidence{}, errors.New("release input changed while being hashed")
	}
	return releases.FileEvidence{
		Path: relative, Length: written, SHA256: hex.EncodeToString(digest.Sum(nil)),
	}, nil
}

func writeEvidenceFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err = file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	written, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func publishEvidenceBundle(staging, output string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, staging, unix.AT_FDCWD, output, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errors.New("release candidate evidence output already exists")
		}
		return err
	}
	return nil
}

func readRegular(path string, maximum int64) ([]byte, error) {
	file, info, err := openRegular(path, maximum)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != info.Size() || int64(len(raw)) > maximum {
		return nil, errors.New("release input changed or exceeded the read bound")
	}
	return raw, nil
}

func openRegular(path string, maximum int64) (*os.File, os.FileInfo, error) {
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
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		file.Close()
		return nil, nil, errors.New("release input must be a bounded regular file")
	}
	return file, info, nil
}

func cleanAbsolute(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value ||
		value == string(filepath.Separator) || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s must be a clean absolute non-root path", name)
	}
	return value, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
