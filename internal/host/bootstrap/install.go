package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"loki/internal/host/releases"
)

type Runner interface {
	Run(context.Context, string, []string) error
}

type ExecRunner struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

func (r ExecRunner) Run(ctx context.Context, executable string, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = r.Stdin
	command.Stdout = r.Stdout
	command.Stderr = r.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run verified Loki host installer: %w", err)
	}
	return nil
}

type Config struct {
	StateRoot       string
	ReleaseTag      string
	ReleaseManifest []byte
	Host            *releases.SupportedHost
	InstallArgs     []string
	Runner          Runner
	Fetcher         AssetFetcher
}

func Run(ctx context.Context, cfg Config) (Candidate, error) {
	if cfg.Runner == nil {
		return Candidate{}, errors.New("bootstrap installer runner is not configured")
	}
	if err := ensureStateRoot(cfg.StateRoot); err != nil {
		return Candidate{}, err
	}
	host := releases.SupportedHost{}
	var err error
	if cfg.Host == nil {
		host, err = releases.DetectHost()
		if err != nil {
			return Candidate{}, err
		}
	} else {
		host = *cfg.Host
	}
	candidate, err := Resolve(ctx, cfg.ReleaseManifest, host, cfg.ReleaseTag, cfg.Fetcher)
	if err != nil {
		return Candidate{}, err
	}
	if err = runCandidate(ctx, cfg.StateRoot, candidate, cfg.InstallArgs, cfg.Runner); err != nil {
		return Candidate{}, err
	}
	return candidate, nil
}

func DefaultStateRoot(system bool) (string, error) {
	if system {
		return "/var/lib/loki/bootstrap", nil
	}
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(base) || filepath.Clean(base) != base || base == string(filepath.Separator) ||
		strings.ContainsRune(base, 0) {
		return "", errors.New("bootstrap state base must be a clean absolute non-root path")
	}
	return filepath.Join(base, "loki", "bootstrap"), nil
}

func runCandidate(ctx context.Context, stateRoot string, candidate Candidate, installArgs []string, runner Runner) error {
	if runner == nil {
		return errors.New("bootstrap installer runner is not configured")
	}
	if err := candidate.Manifest.HostBinary.VerifyBytes(candidate.Binary); err != nil {
		return fmt.Errorf("verify bootstrap host binary: %w", err)
	}
	verifiedManifest, err := releases.LoadReleaseManifest(candidate.ManifestBytes)
	if err != nil {
		return fmt.Errorf("verify embedded release manifest: %w", err)
	}
	if verifiedManifest.Generation.ID != candidate.Manifest.Generation.ID {
		return errors.New("bootstrap release manifest identity changed after resolution")
	}
	stagingRoot, err := os.MkdirTemp(stateRoot, ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stagingRoot)
	if err = os.Chmod(stagingRoot, 0700); err != nil {
		return err
	}
	binaryPath := filepath.Join(stagingRoot, "loki")
	if err = writeStagedFile(binaryPath, candidate.Binary, 0700); err != nil {
		return err
	}
	manifestPath := filepath.Join(stagingRoot, "release-manifest.json")
	if err = writeStagedFile(manifestPath, candidate.ManifestBytes, 0600); err != nil {
		return err
	}
	args := make([]string, 0, 4+len(installArgs))
	args = append(args, "host", "install", "--bootstrap-release-manifest", manifestPath)
	args = append(args, installArgs...)
	if err = runner.Run(ctx, binaryPath, args); err != nil {
		return err
	}
	return ctx.Err()
}

func writeStagedFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func ensureStateRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) ||
		strings.ContainsRune(root, 0) {
		return errors.New("bootstrap state root must be a clean absolute non-root path")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0700 != 0700 {
		return errors.New("bootstrap state root must be a private owner-accessible real directory")
	}
	return nil
}
