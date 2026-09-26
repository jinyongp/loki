package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/host/assets"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/host/releases"
)

var (
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	imagePattern   = regexp.MustCompile(`^ghcr\.io/jinyongp/(loki|loki-browser)@sha256:[0-9a-f]{64}$`)
)

type options struct {
	Output         string
	Version        string
	SourceRevision string
	ReleasedAt     string
	CoreImage      string
	BrowserImage   string
	ReleaseNotes   string
	SourceRoot     string
}

type buildRunner interface {
	BuildHost(context.Context, string, string, string, string, string) error
	BuildBootstrap(context.Context, string, string, string, string) error
}

type execBuildRunner struct{}

func (execBuildRunner) BuildHost(ctx context.Context, root, output, version, revision, date string) error {
	ldflags := strings.Join([]string{
		"-s", "-w",
		"-X", "loki/internal/buildinfo.Version=" + version,
		"-X", "loki/internal/buildinfo.Commit=" + revision,
		"-X", "loki/internal/buildinfo.Date=" + date,
	}, " ")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ldflags, "-o", output, "./cmd/loki")
	cmd.Dir = root
	cmd.Env = append(filteredEnv(os.Environ(), "GOOS", "GOARCH", "CGO_ENABLED"),
		"GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build host binary: %w", err)
	}
	return nil
}

func (execBuildRunner) BuildBootstrap(ctx context.Context, root, output, tag, manifest string) error {
	cmd := exec.CommandContext(ctx, "go", "run", "./tools/release/bootstrapbuild",
		"--output", output,
		"--release-tag", tag,
		"--release-manifest", manifest,
		"--source-root", root,
	)
	cmd.Dir = root
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build release bootstrap: %w", err)
	}
	return nil
}

func main() {
	var cfg options
	flag.StringVar(&cfg.Output, "output", "", "absolute output directory")
	flag.StringVar(&cfg.Version, "version", "", "release version without v prefix")
	flag.StringVar(&cfg.SourceRevision, "source-revision", "", "full source commit SHA")
	flag.StringVar(&cfg.ReleasedAt, "released-at", "", "RFC3339 release timestamp")
	flag.StringVar(&cfg.CoreImage, "core-image", "", "digest-pinned core OCI image")
	flag.StringVar(&cfg.BrowserImage, "browser-image", "", "digest-pinned browser OCI image")
	flag.StringVar(&cfg.ReleaseNotes, "release-notes", "", "release notes Markdown")
	flag.StringVar(&cfg.SourceRoot, "source-root", "", "repository root")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if err := assemble(context.Background(), cfg, execBuildRunner{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func assemble(ctx context.Context, cfg options, runner buildRunner) error {
	if runner == nil {
		return errors.New("release build runner is not configured")
	}
	root, err := resolveSourceRoot(cfg.SourceRoot)
	if err != nil {
		return err
	}
	output, err := cleanAbsolute(cfg.Output, "output")
	if err != nil {
		return err
	}
	if _, err = os.Lstat(output); err == nil {
		return errors.New("release output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	version := strings.TrimSpace(cfg.Version)
	if !versionPattern.MatchString(version) {
		return errors.New("release version must be canonical MAJOR.MINOR.PATCH")
	}
	revision := strings.ToLower(strings.TrimSpace(cfg.SourceRevision))
	if !commitPattern.MatchString(revision) {
		return errors.New("source revision must be a full lowercase Git SHA")
	}
	releasedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(cfg.ReleasedAt))
	if err != nil {
		return errors.New("released-at must be RFC3339")
	}
	releasedAt = releasedAt.UTC().Truncate(time.Second)
	coreDigest, err := imageDigest(strings.TrimSpace(cfg.CoreImage), "loki")
	if err != nil {
		return err
	}
	browserDigest, err := imageDigest(strings.TrimSpace(cfg.BrowserImage), "loki-browser")
	if err != nil {
		return err
	}
	notesPath, err := cleanAbsolute(cfg.ReleaseNotes, "release notes")
	if err != nil {
		return err
	}
	notes, err := os.ReadFile(notesPath)
	if err != nil || len(bytes.TrimSpace(notes)) == 0 {
		return errors.New("release notes are missing or empty")
	}

	parent := filepath.Dir(output)
	if info, statErr := os.Lstat(parent); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("release output parent must be a real directory")
	}
	temp, err := os.MkdirTemp(parent, ".loki-release-")
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

	hostPath := filepath.Join(temp, "loki-linux-amd64")
	if err = runner.BuildHost(ctx, root, hostPath, version, revision, releasedAt.Format(time.RFC3339)); err != nil {
		return err
	}
	hostRaw, err := readRegular(hostPath, 1<<30)
	if err != nil {
		return err
	}

	hostAssetsRaw, err := buildHostAssets()
	if err != nil {
		return err
	}
	toolchainRaw, err := os.ReadFile(filepath.Join(root, "packaging", "native", "toolchain-catalog.json"))
	if err != nil {
		return err
	}
	effectiveConfig, err := os.ReadFile(filepath.Join(root, "config", "runtime.toml"))
	if err != nil {
		return err
	}
	policy, _, err := hostpolicy.CompileFiles(
		filepath.Join(root, "config", "runtime.toml"),
		filepath.Join(root, "config", "github.compose.toml"),
		filepath.Join(root, "packaging", "native", "execution-contract.json"),
	)
	if err != nil {
		return fmt.Errorf("compile release policy: %w", err)
	}
	effectivePolicy := policy.CanonicalJSON()

	licenseRaw, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		return err
	}
	wslPackageInventory, err := buildWSLPackageInventory(root)
	if err != nil {
		return err
	}
	noticesRaw, _, err := releases.BuildNoticeBundle(
		[]releases.NoticeRequirement{{Component: "loki", Version: version}},
		[]releases.NoticeMaterial{
			{Component: "loki", Version: version, Kind: "license", Filename: "LICENSE", Data: licenseRaw},
			{Component: "loki", Version: version, Kind: "notice", Filename: "WSL-PACKAGES.txt", Data: wslPackageInventory},
		},
	)
	if err != nil {
		return err
	}

	subjects := []releases.ProvenanceSubject{
		subject("loki-linux-amd64", hostRaw),
		subject("loki-host-assets.tar.gz", hostAssetsRaw),
		subject("loki-toolchain-catalog.json", toolchainRaw),
		subject("loki-notices.tar.gz", noticesRaw),
		subject("loki-release-notes.md", notes),
	}
	sourceDigest := sha256.Sum256([]byte(revision))
	provenanceRaw, err := releases.BuildSLSAProvenanceStatement(releases.ProvenanceBuild{
		BuildType:    "https://github.com/jinyongp/loki/.github/workflows/release.yml",
		BuilderID:    "https://github.com/actions/runner",
		InvocationID: revision + ":" + version,
		StartedAt:    releasedAt,
		FinishedAt:   releasedAt,
		ExternalParameters: map[string]string{
			"commit":  revision,
			"ref":     "refs/tags/v" + version,
			"release": version,
		},
		ResolvedInputs: []releases.ProvenanceSubject{{
			Name: "source-revision", SHA256: hex.EncodeToString(sourceDigest[:]),
		}},
		Subjects: subjects,
	})
	if err != nil {
		return err
	}

	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version:          version,
		ReleasedAt:       releasedAt,
		HostBinaryDigest: "sha256:" + digest(hostRaw),
		CoreImageDigest:  coreDigest,
		Components: []releases.Component{{
			Name: "browser", Digest: browserDigest, Optional: true,
		}},
		ConfigSchema: 1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: releases.Compatibility{
			Config:    releases.SchemaRange{Min: 1, Max: 1},
			Policy:    releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1},
			State:     releases.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: releases.RollbackCoverage{
			StateSnapshot: true, ConfigSnapshot: true,
			OptionalComponentState: []string{"browser"},
		},
	})
	if err != nil {
		return err
	}
	manifest, err := releases.NewReleaseManifest(releases.ReleaseManifest{
		Version:          releases.ReleaseManifestVersion,
		Generation:       generation,
		HostBinary:       descriptor("releases/bin/loki-"+version, hostRaw),
		HostAssets:       descriptor("releases/assets/loki-host-"+version+".tar.gz", hostAssetsRaw),
		ToolchainCatalog: descriptor("toolchains/catalogs/"+version+".json", toolchainRaw),
		Provenance:       descriptor("releases/provenance/"+version+".bundle.json", provenanceRaw),
		Notices:          descriptor("releases/notices/"+version+".tar.gz", noticesRaw),
		ReleaseNotes:     descriptor("releases/notes/"+version+".md", notes),
		SupportedHosts: []releases.SupportedHost{
			{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
			{Environment: "wsl", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
		},
		Runtime: releases.RuntimeRequirements{DockerMin: "29.8.1", ComposeMin: "5.5.1"},
	})
	if err != nil {
		return err
	}
	manifestRaw, err := releases.EncodeReleaseManifest(manifest)
	if err != nil {
		return err
	}
	manifestDescriptor := descriptor("releases/manifests/"+version+".json", manifestRaw)
	entry, err := releases.IndexEntryForManifest(manifest, manifestDescriptor)
	if err != nil {
		return err
	}
	index, err := releases.NewReleaseIndex([]releases.ReleaseIndexEntry{entry})
	if err != nil {
		return err
	}
	indexRaw, err := encodeJSON(index)
	if err != nil {
		return err
	}

	files := []struct {
		name string
		raw  []byte
		mode os.FileMode
	}{
		{"host-assets.tar.gz", hostAssetsRaw, 0644},
		{"toolchain-catalog.json", toolchainRaw, 0644},
		{"provenance.bundle.json", provenanceRaw, 0644},
		{"notices.tar.gz", noticesRaw, 0644},
		{"release-notes.md", notes, 0644},
		{"effective-policy.json", effectivePolicy, 0644},
		{"effective-config.toml", effectiveConfig, 0644},
		{"release-manifest.json", manifestRaw, 0644},
		{"release-index.json", indexRaw, 0644},
	}
	for _, file := range files {
		if err = writeSynced(filepath.Join(temp, file.name), file.raw, file.mode); err != nil {
			return err
		}
	}
	if err = os.Chmod(hostPath, 0755); err != nil {
		return err
	}
	bootstrapPath := filepath.Join(temp, "loki-bootstrap-linux-amd64")
	if err = runner.BuildBootstrap(ctx, root, bootstrapPath, "v"+version, filepath.Join(temp, "release-manifest.json")); err != nil {
		return err
	}
	if _, err = readRegular(bootstrapPath, 1<<30); err != nil {
		return err
	}
	if err = syncDirectory(temp); err != nil {
		return err
	}
	if err = unix.Renameat2(unix.AT_FDCWD, temp, unix.AT_FDCWD, output, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errors.New("release output already exists")
		}
		return err
	}
	cleanup = false
	return syncDirectory(parent)
}

func buildWSLPackageInventory(root string) ([]byte, error) {
	dockerfileRaw, err := os.ReadFile(filepath.Join(root, "packaging", "wsl", "Dockerfile"))
	if err != nil {
		return nil, err
	}
	const prefix = "FROM --platform=linux/amd64 "
	const suffix = " AS rootfs"
	baseImage := ""
	for _, line := range strings.Split(string(dockerfileRaw), "\n") {
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
			continue
		}
		if baseImage != "" {
			return nil, errors.New("WSL Dockerfile contains multiple rootfs base images")
		}
		baseImage = strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
	}
	if baseImage == "" || strings.Contains(baseImage, "://") ||
		!strings.Contains(baseImage, "@sha256:") {
		return nil, errors.New("WSL Dockerfile rootfs base image is not digest pinned")
	}
	lockRaw, err := os.ReadFile(filepath.Join(root, "packaging", "wsl", "apt-delta.lock"))
	if err != nil {
		return nil, err
	}
	if len(lockRaw) == 0 || lockRaw[len(lockRaw)-1] != '\n' || len(bytes.TrimSpace(lockRaw)) == 0 {
		return nil, errors.New("WSL apt package inventory is empty or non-canonical")
	}
	var output bytes.Buffer
	fmt.Fprintf(&output, "base_image=%s\n", baseImage)
	output.WriteString("added_or_updated_packages:\n")
	output.Write(lockRaw)
	return output.Bytes(), nil
}

func buildHostAssets() ([]byte, error) {
	var buffer bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	gz.Header.ModTime = time.Unix(0, 0).UTC()
	gz.Header.OS = 255
	tw := tar.NewWriter(gz)
	for _, file := range []struct {
		name string
		raw  []byte
	}{
		{"compose.yaml", assets.Compose()},
		{"config/github.compose.toml", assets.GitHubConfig()},
		{"config/ingress.compose.toml", assets.IngressConfig()},
	} {
		header := &tar.Header{Name: file.name, Mode: 0644, Size: int64(len(file.raw)), ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg}
		if err = tw.WriteHeader(header); err != nil {
			return nil, errors.Join(err, tw.Close(), gz.Close())
		}
		if _, err = tw.Write(file.raw); err != nil {
			return nil, errors.Join(err, tw.Close(), gz.Close())
		}
	}
	if err = tw.Close(); err != nil {
		_ = gz.Close()
		return nil, err
	}
	if err = gz.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func descriptor(path string, raw []byte) releases.TargetDescriptor {
	return releases.TargetDescriptor{Path: path, Length: int64(len(raw)), SHA256: digest(raw)}
}

func subject(name string, raw []byte) releases.ProvenanceSubject {
	return releases.ProvenanceSubject{Name: name, SHA256: digest(raw)}
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func imageDigest(ref, repository string) (string, error) {
	if !imagePattern.MatchString(ref) || !strings.HasPrefix(ref, "ghcr.io/jinyongp/"+repository+"@") {
		return "", fmt.Errorf("%s image must be a digest-pinned ghcr.io/jinyongp/%s reference", repository, repository)
	}
	_, value, _ := strings.Cut(ref, "@")
	return value, nil
}

func encodeJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func readRegular(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("release input is not a bounded regular file")
	}
	return os.ReadFile(path)
}

func writeSynced(path string, raw []byte, mode os.FileMode) error {
	if len(raw) == 0 {
		return errors.New("release output file is empty")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func cleanAbsolute(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s must be a clean absolute non-root path", name)
	}
	return value, nil
}

func resolveSourceRoot(value string) (string, error) {
	if strings.TrimSpace(value) != "" {
		root, err := cleanAbsolute(value, "source root")
		if err != nil {
			return "", err
		}
		if _, err = os.Stat(filepath.Join(root, "go.mod")); err != nil {
			return "", errors.New("source root does not contain go.mod")
		}
		return root, nil
	}
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if info, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr == nil && info.Mode().IsRegular() {
			return root, nil
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", errors.New("repository root not found")
		}
		root = parent
	}
}

func filteredEnv(values []string, names ...string) []string {
	blocked := map[string]bool{}
	for _, name := range names {
		blocked[name] = true
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		if !blocked[key] {
			result = append(result, value)
		}
	}
	return result
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
