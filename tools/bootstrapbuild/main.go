package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"loki/internal/host/releases"
)

const maxBootstrapRootBytes = 512 << 10

var targetTokenPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

type buildOptions struct {
	Output      string
	MetadataURL string
	TrustedRoot string
	SourceRoot  string
	GOOS        string
	GOARCH      string
}

type buildRunner interface {
	Run(context.Context, string, []string, []string) error
}

type execBuildRunner struct{}

func (execBuildRunner) Run(ctx context.Context, cwd string, args, environment []string) error {
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = cwd
	command.Env = environment
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build Loki bootstrap: %w", err)
	}
	return nil
}

func main() {
	var options buildOptions
	flag.StringVar(&options.Output, "output", "", "absolute output path")
	flag.StringVar(&options.MetadataURL, "metadata-url", "", "HTTPS TUF metadata repository URL")
	flag.StringVar(&options.TrustedRoot, "trusted-root", "", "initial TUF root.json path")
	flag.StringVar(&options.SourceRoot, "source-root", "", "repository root (defaults to current repository)")
	flag.StringVar(&options.GOOS, "goos", "linux", "target GOOS")
	flag.StringVar(&options.GOARCH, "goarch", "amd64", "target GOARCH")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if err := buildBootstrap(context.Background(), options, execBuildRunner{}, os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type trustValidator func(string, []byte) error

func buildBootstrap(ctx context.Context, options buildOptions, runner buildRunner, environment []string) error {
	return buildBootstrapWithValidator(ctx, options, runner, environment, validateEmbeddedTrust)
}

func buildBootstrapWithValidator(
	ctx context.Context,
	options buildOptions,
	runner buildRunner,
	environment []string,
	validate trustValidator,
) error {
	if runner == nil {
		return errors.New("bootstrap build runner is not configured")
	}
	if validate == nil {
		return errors.New("bootstrap trust validator is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sourceRoot, err := resolveSourceRoot(options.SourceRoot)
	if err != nil {
		return err
	}
	output, err := validateOutputPath(options.Output)
	if err != nil {
		return err
	}
	rootRaw, err := readTrustedRoot(options.TrustedRoot)
	if err != nil {
		return err
	}
	if err = validate(options.MetadataURL, rootRaw); err != nil {
		return err
	}
	goos := strings.ToLower(strings.TrimSpace(options.GOOS))
	goarch := strings.ToLower(strings.TrimSpace(options.GOARCH))
	if !targetTokenPattern.MatchString(goos) || !targetTokenPattern.MatchString(goarch) {
		return errors.New("bootstrap target GOOS/GOARCH is invalid")
	}

	parent := filepath.Dir(output)
	if err = os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("bootstrap output directory must be a real directory")
	}
	if _, err = os.Lstat(output); err == nil {
		return errors.New("bootstrap output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(parent, ".loki-bootstrap-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err = temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err = os.Remove(temporaryPath); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()

	embeddedRoot := base64.StdEncoding.EncodeToString(rootRaw)
	ldflags := strings.Join([]string{
		"-buildid=",
		"-X", "main.releaseMetadataURL=" + options.MetadataURL,
		"-X", "main.trustedRootBase64=" + embeddedRoot,
	}, " ")
	args := []string{
		"build",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags", ldflags,
		"-o", temporaryPath,
		"./cmd/loki-bootstrap",
	}
	buildEnvironment := filteredBuildEnvironment(environment, goos, goarch)
	if err = runner.Run(ctx, sourceRoot, args, buildEnvironment); err != nil {
		return err
	}
	info, err := os.Lstat(temporaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("bootstrap builder did not produce a regular file")
	}
	if err = os.Chmod(temporaryPath, 0755); err != nil {
		return err
	}
	file, err := os.Open(temporaryPath)
	if err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(temporaryPath, output); err != nil {
		return err
	}
	cleanup = false
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func resolveSourceRoot(value string) (string, error) {
	if strings.TrimSpace(value) != "" {
		value = filepath.Clean(strings.TrimSpace(value))
		if !filepath.IsAbs(value) || value == string(filepath.Separator) {
			return "", errors.New("bootstrap source root must be a clean absolute non-root path")
		}
		if _, err := os.Stat(filepath.Join(value, "go.mod")); err != nil {
			return "", errors.New("bootstrap source root does not contain go.mod")
		}
		return value, nil
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

func validateOutputPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) ||
		strings.ContainsRune(value, 0) {
		return "", errors.New("bootstrap output must be a clean absolute non-root path")
	}
	return value, nil
}

func readTrustedRoot(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) ||
		strings.ContainsRune(path, 0) {
		return nil, errors.New("bootstrap trusted root path must be a clean absolute non-root path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maxBootstrapRootBytes {
		return nil, errors.New("bootstrap trusted root must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBootstrapRootBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxBootstrapRootBytes {
		return nil, errors.New("bootstrap trusted root exceeds size policy")
	}
	return raw, nil
}

func validateEmbeddedTrust(metadataURL string, root []byte) error {
	stateRoot, err := os.MkdirTemp("", "loki-bootstrap-trust-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stateRoot)
	if err = os.Chmod(stateRoot, 0700); err != nil {
		return err
	}
	client, err := releases.Open(releases.Config{
		StateRoot:         stateRoot,
		RemoteMetadataURL: strings.TrimSpace(metadataURL),
		TrustedRoot:       root,
	})
	if err != nil {
		return fmt.Errorf("validate bootstrap trust: %w", err)
	}
	if client == nil {
		return errors.New("validate bootstrap trust returned no client")
	}
	return nil
}

func filteredBuildEnvironment(environment []string, goos, goarch string) []string {
	result := make([]string, 0, len(environment)+3)
	for _, entry := range environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch key {
		case "CGO_ENABLED", "GOOS", "GOARCH":
			continue
		default:
			result = append(result, entry)
		}
	}
	result = append(result, "CGO_ENABLED=0", "GOOS="+goos, "GOARCH="+goarch)
	return result
}
