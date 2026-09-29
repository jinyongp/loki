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
	"loki/internal/progress"
)

type frontendVersionEnvelope struct {
	Version string                     `json:"version"`
	Commit  string                     `json:"commit"`
	Date    string                     `json:"date"`
	Binding windowshost.ReleaseBinding `json:"release_binding"`
}

func runProductUpdate(ctx context.Context, stdout, stderr io.Writer) int {
	reporter := progress.NewLineReporter(stderr)
	client := windowshost.FrontendReleaseClient{Progress: reporter}
	return runProductUpdateWith(ctx, productUpdateDependencies{
		CurrentBinding:    windowshost.CurrentReleaseBinding,
		Client:            client,
		Stage:             stageFrontendUpdateCandidate,
		Inspect:           inspectFrontendCandidate,
		RunCandidate:      runFrontendCandidate,
		CanonicalPath:     canonicalFrontendPath,
		ApplianceAdvisory: reportWindowsApplianceUpdateAdvisory,
		Progress:          reporter,
	}, stdout, stderr)
}

func reportWindowsApplianceUpdateAdvisory(ctx context.Context, stdout io.Writer) error {
	distribution := defaultDistribution()
	operator := windowshost.NewWindowsOperatorClient()
	releases := windowshost.ApplianceReleaseClient{}
	advisory, err := resolveApplianceUpdateAdvisory(ctx, applianceAdvisoryDependencies{
		Status: func(ctx context.Context) (windowshost.OperatorStatus, error) {
			result, err := operator.Execute(ctx, distribution, windowshost.OperatorRequest{Command: "status"})
			if err != nil {
				return windowshost.OperatorStatus{}, err
			}
			if result.Probe.ExitCode != 0 {
				detail := strings.TrimSpace(result.Probe.Stderr)
				if detail == "" {
					detail = strings.TrimSpace(result.Probe.Stdout)
				}
				return windowshost.OperatorStatus{}, fmt.Errorf("inspect Loki appliance status: exit %d: %s", result.Probe.ExitCode, detail)
			}
			return windowshost.ParseOperatorStatus([]byte(result.Probe.Stdout))
		},
		Resolve: releases.Resolve,
		Compare: releases.Compare,
	})
	if err != nil {
		return err
	}
	return renderApplianceUpdateAdvisory(advisory, stdout)
}

func canonicalFrontendPath() (string, error) {
	localAppData, _, err := windowsEnvironmentRoots()
	if err != nil {
		return "", err
	}
	paths, err := windowshost.ResolveFrontendPaths(localAppData)
	if err != nil {
		return "", err
	}
	return paths.Binary, nil
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
