package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/host/connect"
)

const (
	maxWSLBytes     = int64(1 << 30)
	maxCatalogBytes = int64(4 << 20)
)

var (
	releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	revisionPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type options struct {
	Output         string
	ReleaseTag     string
	SourceRevision string
	ReleasedAt     string
	WSLAppliance   string
	HelperCatalog  string
	SourceRoot     string
}

type fileIdentity struct {
	SHA256 string
	Length int64
}

type buildRunner interface {
	Build(context.Context, buildRequest) error
}

type buildRequest struct {
	SourceRoot     string
	Output         string
	Version        string
	SourceRevision string
	ReleasedAt     string
	WSL            fileIdentity
	HelperCatalog  fileIdentity
}

type execBuildRunner struct{}

func (execBuildRunner) Build(ctx context.Context, request buildRequest) error {
	buildDir, err := os.MkdirTemp("", "loki-keepalive-build-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildDir)
	companion := filepath.Join(buildDir, "loki-keepalive.exe")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w -H=windowsgui", "-o", companion, "./cmd/loki-keepalive")
	build.Dir = request.SourceRoot
	build.Env = append(filteredEnv(os.Environ(), "GOOS", "GOARCH", "CGO_ENABLED"), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err = build.Run(); err != nil {
		return fmt.Errorf("build console-free WSL keepalive: %w", err)
	}
	raw, err := os.ReadFile(companion)
	if err != nil {
		return err
	}
	payload := filepath.Join(buildDir, "keepalive_payload.go")
	if err = os.WriteFile(payload, []byte("package windows\nconst keepaliveExecutableBase64 = "+strconv.Quote(base64.StdEncoding.EncodeToString(raw))+"\n"), 0o600); err != nil {
		return err
	}
	overlay := filepath.Join(buildDir, "overlay.json")
	manifest, err := json.Marshal(map[string]any{"Replace": map[string]string{filepath.Join(request.SourceRoot, "internal", "host", "windows", "keepalive_payload.go"): payload}})
	if err != nil {
		return err
	}
	if err = os.WriteFile(overlay, manifest, 0o600); err != nil {
		return err
	}
	ldflags := strings.Join([]string{
		"-s", "-w",
		"-X", "loki/internal/buildinfo.Version=" + request.Version,
		"-X", "loki/internal/buildinfo.Commit=" + request.SourceRevision,
		"-X", "loki/internal/buildinfo.Date=" + request.ReleasedAt,
		"-X", "loki/internal/host/windows.WSLApplianceSHA256=" + request.WSL.SHA256,
		"-X", "loki/internal/host/windows.WSLApplianceLength=" + strconv.FormatInt(request.WSL.Length, 10),
		"-X", "loki/internal/host/windows.HelperCatalogSHA256=" + request.HelperCatalog.SHA256,
		"-X", "loki/internal/host/windows.HelperCatalogLength=" + strconv.FormatInt(request.HelperCatalog.Length, 10),
	}, " ")
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-buildvcs=false", "-overlay", overlay, "-ldflags", ldflags, "-o", request.Output, "./cmd/loki-windows")
	cmd.Dir = request.SourceRoot
	cmd.Env = append(filteredEnv(os.Environ(), "GOOS", "GOARCH", "CGO_ENABLED"),
		"GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build Windows frontend: %w", err)
	}
	return nil
}

func main() {
	var cfg options
	flag.StringVar(&cfg.Output, "output", "", "absolute output path")
	flag.StringVar(&cfg.ReleaseTag, "release-tag", "", "release tag")
	flag.StringVar(&cfg.SourceRevision, "source-revision", "", "full source commit SHA")
	flag.StringVar(&cfg.ReleasedAt, "released-at", "", "RFC3339 release timestamp")
	flag.StringVar(&cfg.WSLAppliance, "wsl-appliance", "", "accepted WSL appliance path")
	flag.StringVar(&cfg.HelperCatalog, "helper-catalog", "", "connect helper catalog path")
	flag.StringVar(&cfg.SourceRoot, "source-root", "", "source root")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if err := buildWindowsFrontend(context.Background(), cfg, execBuildRunner{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func buildWindowsFrontend(ctx context.Context, cfg options, runner buildRunner) error {
	if !releaseTagPattern.MatchString(strings.TrimSpace(cfg.ReleaseTag)) {
		return errors.New("Windows frontend release tag is invalid")
	}
	revision := strings.ToLower(strings.TrimSpace(cfg.SourceRevision))
	if !revisionPattern.MatchString(revision) {
		return errors.New("Windows frontend source revision is invalid")
	}
	releasedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(cfg.ReleasedAt))
	if err != nil {
		return errors.New("Windows frontend release timestamp is invalid")
	}
	output, err := cleanAbsolute(cfg.Output, "output")
	if err != nil {
		return err
	}
	root, err := cleanAbsolute(cfg.SourceRoot, "source root")
	if err != nil {
		return err
	}
	wsl, err := cleanAbsolute(cfg.WSLAppliance, "WSL appliance")
	if err != nil {
		return err
	}
	catalogPath, err := cleanAbsolute(cfg.HelperCatalog, "helper catalog")
	if err != nil {
		return err
	}
	if _, err = os.Lstat(output); err == nil {
		return errors.New("Windows frontend output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info, statErr := os.Lstat(filepath.Dir(output)); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Windows frontend output parent must be a real directory")
	}
	if info, statErr := os.Lstat(root); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Windows frontend source root must be a real directory")
	}

	wslIdentity, _, err := readIdentity(wsl, maxWSLBytes)
	if err != nil {
		return fmt.Errorf("read WSL appliance: %w", err)
	}
	catalogIdentity, catalogRaw, err := readIdentity(catalogPath, maxCatalogBytes)
	if err != nil {
		return fmt.Errorf("read helper catalog: %w", err)
	}
	if _, err = connect.LoadCatalog(catalogRaw); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(output), ".loki-windows-*.exe")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	if err = temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	_ = os.Remove(tempPath)
	defer os.Remove(tempPath)

	request := buildRequest{
		SourceRoot:     root,
		Output:         tempPath,
		Version:        strings.TrimPrefix(strings.TrimSpace(cfg.ReleaseTag), "v"),
		SourceRevision: revision,
		ReleasedAt:     releasedAt.UTC().Truncate(time.Second).Format(time.RFC3339),
		WSL:            wslIdentity,
		HelperCatalog:  catalogIdentity,
	}
	if err = runner.Build(ctx, request); err != nil {
		return err
	}
	info, err := os.Lstat(tempPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 {
		return errors.New("Windows frontend build did not produce a regular non-empty file")
	}
	built, err := os.Open(tempPath)
	if err != nil {
		return err
	}
	syncErr := built.Sync()
	closeErr := built.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = unix.Renameat2(unix.AT_FDCWD, tempPath, unix.AT_FDCWD, output, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errors.New("Windows frontend output already exists")
		}
		return err
	}
	return syncDirectory(filepath.Dir(output))
}

func readIdentity(path string, limit int64) (fileIdentity, []byte, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, nil, err
	}
	if !linkInfo.Mode().IsRegular() || linkInfo.Mode()&os.ModeSymlink != 0 ||
		linkInfo.Size() <= 0 || linkInfo.Size() > limit {
		return fileIdentity{}, nil, errors.New("file is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return fileIdentity{}, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileIdentity{}, nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != linkInfo.Size() {
		return fileIdentity{}, nil, errors.New("file changed while being opened")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return fileIdentity{}, nil, err
	}
	if int64(len(raw)) != info.Size() {
		return fileIdentity{}, nil, errors.New("file changed while being read")
	}
	sum := sha256.Sum256(raw)
	return fileIdentity{SHA256: hex.EncodeToString(sum[:]), Length: int64(len(raw))}, raw, nil
}

func cleanAbsolute(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) ||
		strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s must be a clean absolute non-root path", label)
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

func filteredEnv(environment []string, names ...string) []string {
	blocked := map[string]bool{}
	for _, name := range names {
		blocked[name+"="] = true
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		skip := false
		for prefix := range blocked {
			if strings.HasPrefix(entry, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
