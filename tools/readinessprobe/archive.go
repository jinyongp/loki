package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"loki/internal/toolchain"
)

type r8Observation struct {
	ChainedSymlinkEscapedStaging bool `json:"chained_symlink_escaped_staging"`
}

type r9Observation struct {
	RequestedMode      string `json:"requested_mode"`
	InstalledMode      string `json:"installed_mode"`
	ExecutableModeKept bool   `json:"executable_mode_preserved"`
}

func observeArchiveInstall() (r8Observation, r9Observation, error) {
	previousUmask := unix.Umask(0o077)
	defer unix.Umask(previousUmask)

	root, err := os.MkdirTemp("", "loki-readiness-archive-")
	if err != nil {
		return r8Observation{}, r9Observation{}, err
	}
	defer os.RemoveAll(root)

	modeObservation, err := observeArchiveMode(filepath.Join(root, "mode"))
	if err != nil {
		return r8Observation{}, r9Observation{}, err
	}
	escapeObservation, err := observeArchiveEscape(filepath.Join(root, "escape"))
	if err != nil {
		return r8Observation{}, r9Observation{}, err
	}
	return escapeObservation, modeObservation, nil
}

func observeArchiveMode(base string) (r9Observation, error) {
	root := filepath.Join(base, "root")
	bundle := filepath.Join(base, "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "artifacts"), 0700); err != nil {
		return r9Observation{}, err
	}
	archivePath := filepath.Join(bundle, "artifacts", "fixture.zip")
	if err := writeZip(archivePath, []zipEntry{{Name: "bin/review", Body: "#!/bin/sh\nexit 0\n", Mode: 0755}}); err != nil {
		return r9Observation{}, err
	}
	manifest, err := archiveManifest(archivePath, "bin/review")
	if err != nil {
		return r9Observation{}, err
	}
	if err = toolchain.InstallArtifacts(context.Background(), manifest, bundle, root); err != nil {
		return r9Observation{}, fmt.Errorf("install regular archive: %w", err)
	}
	info, err := os.Stat(filepath.Join(root, "opt/loki/toolchain/review/1/bin/review"))
	if err != nil {
		return r9Observation{}, err
	}
	installed := fmt.Sprintf("%04o", info.Mode().Perm())
	return r9Observation{RequestedMode: "0755", InstalledMode: installed, ExecutableModeKept: info.Mode().Perm() == 0755}, nil
}

func observeArchiveEscape(base string) (r8Observation, error) {
	root := filepath.Join(base, "root")
	bundle := filepath.Join(base, "bundle")
	if err := os.MkdirAll(filepath.Join(bundle, "artifacts"), 0700); err != nil {
		return r8Observation{}, err
	}
	outside := filepath.Join(root, "opt/loki/toolchain/review/outside")
	if err := os.MkdirAll(outside, 0700); err != nil {
		return r8Observation{}, err
	}
	archivePath := filepath.Join(bundle, "artifacts", "fixture.zip")
	entries := []zipEntry{
		{Name: "dir", Body: ".", Mode: os.ModeSymlink | 0777},
		{Name: "dir/escape", Body: "../outside", Mode: os.ModeSymlink | 0777},
		{Name: "escape/probe.txt", Body: "synthetic archive marker\n", Mode: 0644},
	}
	if err := writeZip(archivePath, entries); err != nil {
		return r8Observation{}, err
	}
	manifest, err := archiveManifest(archivePath, "escape/probe.txt")
	if err != nil {
		return r8Observation{}, err
	}
	// A safe implementation may reject this archive. Rejection is an observation,
	// not a probe failure, because the fixture itself is intentionally hostile.
	_ = toolchain.InstallArtifacts(context.Background(), manifest, bundle, root)
	_, statErr := os.Stat(filepath.Join(outside, "probe.txt"))
	return r8Observation{ChainedSymlinkEscapedStaging: statErr == nil}, nil
}

type zipEntry struct {
	Name string
	Body string
	Mode os.FileMode
}

func writeZip(path string, entries []zipEntry) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	writer := zip.NewWriter(file)
	for _, item := range entries {
		header := &zip.FileHeader{Name: item.Name}
		header.SetMode(item.Mode)
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			writer.Close()
			file.Close()
			return createErr
		}
		if _, createErr = entry.Write([]byte(item.Body)); createErr != nil {
			writer.Close()
			file.Close()
			return createErr
		}
	}
	if err = writer.Close(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func archiveManifest(archivePath, link string) (toolchain.Manifest, error) {
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return toolchain.Manifest{}, err
	}
	sum := sha256.Sum256(data)
	return toolchain.Manifest{
		Version:     1,
		Platform:    toolchain.Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"},
		AptPackages: []toolchain.AptPackage{{Name: "ca-certificates", Version: "fixture"}},
		Artifacts: []toolchain.Artifact{{
			Name: "review", Version: "1", Filename: "fixture.zip",
			URL: "https://example.invalid/fixture.zip", SHA256: hex.EncodeToString(sum[:]),
			Format: "zip", InstallPath: "/opt/loki/toolchain/review/1",
			Links: map[string]string{"review": link},
		}},
	}, nil
}
