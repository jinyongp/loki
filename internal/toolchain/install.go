package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type installedArtifact struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	SHA256     string `json:"sha256"`
	TreeSHA256 string `json:"tree_sha256"`
}

func InstallArtifacts(ctx context.Context, manifest Manifest, bundle, root string) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if !filepath.IsAbs(bundle) || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("bundle and root must be clean absolute paths")
	}
	for _, artifact := range manifest.Artifacts {
		source := filepath.Join(bundle, "artifacts", artifact.Filename)
		got, err := fileDigest(source)
		if err != nil {
			return fmt.Errorf("verify %s: %w", artifact.Name, err)
		}
		if got != artifact.SHA256 {
			return fmt.Errorf("artifact %s checksum mismatch", artifact.Name)
		}
		if err = installArtifact(ctx, root, source, artifact); err != nil {
			return err
		}
	}
	return installLinks(root, manifest.Artifacts)
}

func InstallMetadata(raw []byte, manifest Manifest, root string) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	provenance := Provenance{Version: 1, ManifestSHA: digest(raw)}
	for _, artifact := range manifest.Artifacts {
		provenance.Artifacts = append(provenance.Artifacts, ProvenanceArtifact{Name: artifact.Name, URL: artifact.URL, SHA256: artifact.SHA256})
	}
	encoded, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return err
	}
	metadata := []struct {
		path string
		data []byte
	}{
		{path: "/opt/loki/toolchain/provenance.json", data: append(encoded, '\n')},
		{path: "/usr/share/doc/loki/toolchain-manifest.json", data: raw},
	}
	for _, item := range metadata {
		target := rooted(root, item.path)
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		temporary, err := os.CreateTemp(filepath.Dir(target), ".toolchain-metadata-")
		if err != nil {
			return err
		}
		name := temporary.Name()
		if _, err = temporary.Write(item.data); err == nil {
			err = temporary.Chmod(0644)
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(name, target)
		}
		if err != nil {
			_ = os.Remove(name)
			return err
		}
	}
	return nil
}

func InstallApt(ctx context.Context, manifest Manifest) error {
	if os.Geteuid() != 0 {
		return errors.New("apt installation requires root")
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	environment := append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	if err := runWithEnvironment(ctx, environment, "apt-get", "update"); err != nil {
		return err
	}
	arguments := []string{"install", "-y", "--no-install-recommends"}
	for _, item := range manifest.AptPackages {
		arguments = append(arguments, item.Name+"="+item.Version)
	}
	return runWithEnvironment(ctx, environment, "apt-get", arguments...)
}

func installArtifact(ctx context.Context, root, source string, artifact Artifact) error {
	target := rooted(root, artifact.InstallPath)
	marker := target + ".loki-artifact.json"
	if artifact.Format != "file" {
		marker = filepath.Join(target, ".loki-artifact.json")
	}
	want := installedArtifact{Name: artifact.Name, Version: artifact.Version, SHA256: artifact.SHA256}
	if _, err := os.Lstat(target); err == nil {
		var got installedArtifact
		raw, readErr := os.ReadFile(marker)
		if readErr == nil {
			readErr = json.Unmarshal(raw, &got)
		}
		if readErr == nil && got.Name == want.Name && got.Version == want.Version && got.SHA256 == want.SHA256 {
			current, digestErr := installedDigest(target, artifact.Format)
			if digestErr == nil && current == got.TreeSHA256 {
				return nil
			}
		}
		return fmt.Errorf("toolchain install path %s is occupied", artifact.InstallPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	if artifact.Format == "file" {
		temporary, err := os.CreateTemp(parent, "."+filepath.Base(target)+"-")
		if err != nil {
			return err
		}
		name := temporary.Name()
		defer os.Remove(name)
		input, err := os.Open(source)
		if err == nil {
			_, err = io.Copy(temporary, input)
			_ = input.Close()
		}
		if err == nil {
			err = temporary.Chmod(0755)
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		if err = os.Rename(name, target); err != nil {
			return err
		}
		want.TreeSHA256 = artifact.SHA256
		raw, _ := json.Marshal(want)
		return os.WriteFile(marker, append(raw, '\n'), 0644)
	}
	temporary, err := os.MkdirTemp(parent, "."+filepath.Base(target)+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	arguments := []string{"-xf", source, "-C", temporary, "--strip-components=" + strconv.Itoa(artifact.StripComponents), "--no-same-owner", "--no-same-permissions"}
	if err = run(ctx, "tar", arguments...); err != nil {
		return fmt.Errorf("extract %s: %w", artifact.Name, err)
	}
	want.TreeSHA256, err = treeDigest(temporary)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(want)
	if err = os.WriteFile(filepath.Join(temporary, ".loki-artifact.json"), append(raw, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(temporary, target)
}

func installLinks(root string, artifacts []Artifact) error {
	bin := rooted(root, "/opt/loki/toolchain/bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		for name, relative := range artifact.Links {
			canonical := artifact.InstallPath
			if relative != "." {
				canonical = filepath.Join(canonical, relative)
			}
			link := filepath.Join(bin, name)
			if current, err := os.Readlink(link); err == nil && current == canonical {
				continue
			}
			if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("toolchain link %s is occupied", name)
			}
			temporary := link + ".new"
			_ = os.Remove(temporary)
			if err := os.Symlink(canonical, temporary); err != nil {
				return err
			}
			if err := os.Rename(temporary, link); err != nil {
				_ = os.Remove(temporary)
				return err
			}
		}
	}
	return nil
}

func rooted(root, path string) string { return filepath.Join(root, strings.TrimPrefix(path, "/")) }

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func installedDigest(target, format string) (string, error) {
	if format == "file" {
		return fileDigest(target)
	}
	return treeDigest(target)
}

func treeDigest(root string) (string, error) {
	hash := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || relative == ".loki-artifact.json" {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%o\x00", filepath.ToSlash(relative), info.Mode())
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hash, target)
		} else if info.Mode().IsRegular() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(hash, file)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		_, _ = io.WriteString(hash, "\x00")
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func run(ctx context.Context, name string, arguments ...string) error {
	return runWithEnvironment(ctx, nil, name, arguments...)
}

func runWithEnvironment(ctx context.Context, environment []string, name string, arguments ...string) error {
	command := exec.CommandContext(ctx, name, arguments...)
	if environment != nil {
		command.Env = environment
	}
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}
