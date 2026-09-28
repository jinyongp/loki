//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	windowshost "loki/internal/host/windows"
)

type frontendVersionEnvelope struct {
	Version string                     `json:"version"`
	Commit  string                     `json:"commit"`
	Date    string                     `json:"date"`
	Binding windowshost.ReleaseBinding `json:"release_binding"`
}

func runProductUpdate(ctx context.Context, stdout, stderr io.Writer) int {
	current, err := windowshost.CurrentReleaseBinding()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client := windowshost.FrontendReleaseClient{}
	pointer, err := client.Resolve(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	comparison, err := client.Compare(current.ReleaseTag, pointer)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if comparison > 0 {
		fmt.Fprintf(stderr, "installed Windows frontend %s is newer than published release %s; refusing downgrade\n",
			current.ReleaseTag, pointer.ReleaseTag)
		return 1
	}
	if comparison == 0 {
		fmt.Fprintf(stdout, "Windows Loki frontend is current at %s.\n", current.ReleaseTag)
		return runInstall(ctx, nil, stdout, stderr)
	}

	raw, err := client.Download(ctx, pointer)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	candidate, cleanup, err := stageFrontendUpdateCandidate(ctx, raw, pointer)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()

	candidateBinding, err := inspectFrontendCandidate(ctx, candidate)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if candidateBinding.ReleaseTag != pointer.ReleaseTag {
		fmt.Fprintf(stderr, "downloaded Windows frontend binding %s does not match published release %s\n",
			candidateBinding.ReleaseTag, pointer.ReleaseTag)
		return 1
	}

	fmt.Fprintf(stdout, "Updating Windows Loki frontend %s -> %s...\n", current.ReleaseTag, pointer.ReleaseTag)
	if code := runFrontendCandidate(ctx, candidate, []string{"bootstrap", "install"}, stdout, stderr); code != 0 {
		return code
	}

	localAppData, _, err := windowsEnvironmentRoots()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	paths, err := windowshost.ResolveFrontendPaths(localAppData)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	installedBinding, err := inspectFrontendCandidate(ctx, paths.Binary)
	if err != nil {
		fmt.Fprintln(stderr, "verify updated Windows frontend:", err)
		return 1
	}
	if installedBinding.ReleaseTag != pointer.ReleaseTag {
		fmt.Fprintf(stderr, "updated Windows frontend is %s; expected %s\n",
			installedBinding.ReleaseTag, pointer.ReleaseTag)
		return 1
	}
	fmt.Fprintf(stdout, "Loki is updated to %s.\n", pointer.ReleaseTag)
	return 0
}

func stageFrontendUpdateCandidate(
	ctx context.Context,
	raw []byte,
	pointer windowshost.FrontendReleasePointer,
) (string, func(), error) {
	localAppData, _, err := windowsEnvironmentRoots()
	if err != nil {
		return "", func() {}, err
	}
	paths, err := windowshost.ResolveFrontendPaths(localAppData)
	if err != nil {
		return "", func() {}, err
	}
	platform := windowshost.NewWindowsFrontendPlatform()
	if err = platform.EnsurePrivateDirectory(ctx, paths.Root); err != nil {
		return "", func() {}, fmt.Errorf("prepare Windows frontend update root: %w", err)
	}
	staging, err := os.MkdirTemp(paths.Root, ".loki-update-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create Windows frontend update staging: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	if err = platform.EnsurePrivateDirectory(ctx, staging); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("protect Windows frontend update staging: %w", err)
	}
	candidate := filepath.Join(staging, "loki-windows-amd64.exe")
	if err = platform.WriteProtectedAtomic(ctx, candidate, raw); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write Windows frontend update candidate: %w", err)
	}
	digest, length, err := platform.FileDigest(candidate)
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("verify staged Windows frontend update: %w", err)
	}
	if digest != pointer.SHA256 || length != pointer.Length {
		cleanup()
		return "", func() {}, errors.New("staged Windows frontend update changed after verification")
	}
	return candidate, cleanup, nil
}

func inspectFrontendCandidate(ctx context.Context, executable string) (windowshost.ReleaseBinding, error) {
	command := exec.CommandContext(ctx, executable, "version", "--json")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		detail := stderr.String()
		if detail == "" {
			detail = stdout.String()
		}
		return windowshost.ReleaseBinding{}, fmt.Errorf("inspect Windows frontend candidate: %w: %s", err, detail)
	}
	var envelope frontendVersionEnvelope
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	if err = decoder.Decode(&envelope); err != nil {
		return windowshost.ReleaseBinding{}, fmt.Errorf("decode Windows frontend candidate identity: %w", err)
	}
	if err = requireJSONEnd(decoder); err != nil {
		return windowshost.ReleaseBinding{}, err
	}
	if err = windowshost.ValidateReleaseBinding(envelope.Binding); err != nil {
		return windowshost.ReleaseBinding{}, err
	}
	if "v"+strings.TrimPrefix(strings.TrimSpace(envelope.Version), "v") != envelope.Binding.ReleaseTag ||
		strings.TrimSpace(envelope.Commit) != envelope.Binding.SourceRevision ||
		strings.TrimSpace(envelope.Date) != envelope.Binding.BuiltAt {
		return windowshost.ReleaseBinding{}, errors.New("Windows frontend candidate version identity does not match its release binding")
	}
	return envelope.Binding, nil
}

func runFrontendCandidate(
	ctx context.Context,
	executable string,
	args []string,
	stdout, stderr io.Writer,
) int {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	fmt.Fprintln(stderr, "start verified Windows frontend update:", err)
	return 1
}
