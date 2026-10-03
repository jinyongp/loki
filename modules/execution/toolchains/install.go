package toolchain

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
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
				if artifact.Format != "file" {
					return os.Chmod(target, 0755)
				}
				return nil
			}
		}
		return fmt.Errorf("toolchain install path %s is occupied", artifact.InstallPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(target)
	if err := makeInstallDirectories(root, parent); err != nil {
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
			permissions := os.FileMode(0644)
			if len(artifact.Links) > 0 {
				permissions = 0755
			}
			err = temporary.Chmod(permissions)
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
	if artifact.Format == "zip" {
		err = extractZip(source, temporary, artifact.StripComponents)
	} else {
		arguments := []string{"-xf", source, "-C", temporary, "--strip-components=" + strconv.Itoa(artifact.StripComponents), "--no-same-owner", "--no-same-permissions"}
		err = run(ctx, "tar", arguments...)
	}
	if err != nil {
		return fmt.Errorf("extract %s: %w", artifact.Name, err)
	}
	if err = normalizeExtractedTree(temporary); err != nil {
		return fmt.Errorf("validate extracted %s: %w", artifact.Name, err)
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

type zipExtractEntry struct {
	file     *zip.File
	relative string
	mode     os.FileMode
}

func extractZip(source, destination string, stripComponents int) error {
	archive, err := zip.OpenReader(source)
	if err != nil {
		return err
	}
	defer archive.Close()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()

	items := make([]zipExtractEntry, 0, len(archive.File))
	symlinks := map[string]bool{}
	seen := map[string]bool{}
	var expanded uint64
	for _, entry := range archive.File {
		name := path.Clean(entry.Name)
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe zip path %q", entry.Name)
		}
		parts := strings.Split(name, "/")
		if len(parts) <= stripComponents {
			continue
		}
		relative := path.Join(parts[stripComponents:]...)
		if relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
			return fmt.Errorf("unsafe zip path %q", entry.Name)
		}
		if seen[relative] || relative == ".loki-artifact.json" {
			return fmt.Errorf("duplicate or reserved zip path %q", entry.Name)
		}
		seen[relative] = true
		mode := entry.Mode()
		if !mode.IsDir() {
			if entry.UncompressedSize64 > 1<<30 || expanded > 2<<30-entry.UncompressedSize64 {
				return fmt.Errorf("zip content exceeds extraction limit")
			}
			expanded += entry.UncompressedSize64
		}
		if mode&os.ModeSymlink != 0 {
			if entry.UncompressedSize64 > 4096 {
				return fmt.Errorf("zip symlink target exceeds limit: %q", entry.Name)
			}
			symlinks[relative] = true
		} else if !mode.IsDir() && !mode.IsRegular() {
			return fmt.Errorf("unsupported zip entry %q", entry.Name)
		}
		items = append(items, zipExtractEntry{file: entry, relative: relative, mode: mode})
	}

	// Never interpret an archive entry underneath another archive-provided
	// symlink. This keeps each entry's logical path identical to its on-disk
	// location when symlinks are published in the second pass.
	for _, item := range items {
		for parent := path.Dir(item.relative); parent != "." && parent != "/"; parent = path.Dir(parent) {
			if symlinks[parent] {
				return fmt.Errorf("zip entry %q is nested below symlink %q", item.file.Name, parent)
			}
		}
	}

	for _, item := range items {
		if item.mode&os.ModeSymlink != 0 {
			continue
		}
		relative := filepath.FromSlash(item.relative)
		if item.mode.IsDir() {
			if err = root.MkdirAll(relative, 0700); err != nil {
				return err
			}
			continue
		}
		parent := filepath.Dir(relative)
		if parent != "." {
			if err = root.MkdirAll(parent, 0700); err != nil {
				return err
			}
		}
		input, openErr := item.file.Open()
		if openErr != nil {
			return openErr
		}
		createMode := os.FileMode(0600)
		if item.mode.Perm()&0111 != 0 {
			createMode = 0700
		}
		output, createErr := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, createMode)
		if createErr != nil {
			input.Close()
			return createErr
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, int64(item.file.UncompressedSize64)+1))
		if copyErr == nil {
			// Preserve executable intent even if umask removed creation bits.
			copyErr = output.Chmod(createMode)
		}
		closeInputErr := input.Close()
		closeOutputErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if written != int64(item.file.UncompressedSize64) {
			return fmt.Errorf("zip entry size mismatch: %q", item.file.Name)
		}
		if closeInputErr != nil {
			return closeInputErr
		}
		if closeOutputErr != nil {
			return closeOutputErr
		}
	}

	for _, item := range items {
		if item.mode&os.ModeSymlink == 0 {
			continue
		}
		input, openErr := item.file.Open()
		if openErr != nil {
			return openErr
		}
		data, readErr := io.ReadAll(io.LimitReader(input, 4097))
		closeErr := input.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		linkTarget := string(data)
		resolved := path.Clean(path.Join(path.Dir(item.relative), linkTarget))
		if linkTarget == "" || len(data) > 4096 || path.IsAbs(linkTarget) || resolved == ".." || strings.HasPrefix(resolved, "../") {
			return fmt.Errorf("unsafe zip symlink %q", item.file.Name)
		}
		relative := filepath.FromSlash(item.relative)
		parent := filepath.Dir(relative)
		if parent != "." {
			if err = root.MkdirAll(parent, 0700); err != nil {
				return err
			}
		}
		if err = root.Symlink(linkTarget, relative); err != nil {
			return err
		}
	}
	return nil
}

// normalizeExtractedTree runs only on a private, quiescent staging tree.
// Validate complete symlink resolution before making the root traversable.
func normalizeExtractedTree(directory string) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	var links []string
	err = filepath.WalkDir(directory, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(directory, current)
		if err != nil {
			return err
		}
		if relative == ".loki-artifact.json" {
			return errors.New("archive contains reserved installation marker")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := root.Readlink(relative)
			if err != nil {
				return err
			}
			resolved := filepath.Join(filepath.Dir(relative), target)
			if target == "" || len(target) > 4096 || filepath.IsAbs(target) || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				return fmt.Errorf("unsafe extracted symlink %q", relative)
			}
			links = append(links, relative)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported extracted object %q", relative)
		}
		mode := os.FileMode(0644)
		if info.IsDir() || info.Mode().Perm()&0111 != 0 {
			mode = 0755
		}
		if relative == "." {
			return nil
		}
		file, err := root.Open(relative)
		if err != nil {
			return err
		}
		chmodErr := file.Chmod(mode)
		return errors.Join(chmodErr, file.Close())
	})
	if err != nil {
		return err
	}
	for _, link := range links {
		// Stat follows the entire chain under the pinned root, including ..
		// after link expansion. Lexical cleaning alone cannot prove this.
		info, err := root.Stat(link)
		if err != nil {
			return fmt.Errorf("unresolvable or unsafe extracted symlink %q: %w", link, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported extracted symlink target %q", link)
		}
	}
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	chmodErr := file.Chmod(0755)
	return errors.Join(chmodErr, file.Close())
}

func installLinks(root string, artifacts []Artifact) error {
	bin := rooted(root, "/opt/loki/toolchain/bin")
	if err := makeInstallDirectories(root, bin); err != nil {
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

// makeInstallDirectories sets modes only on newly created install ancestors.
// It never broadens permissions on existing host directories or the given root.
func makeInstallDirectories(directory, destination string) error {
	relative, err := filepath.Rel(directory, destination)
	if err != nil || !filepath.IsLocal(relative) {
		return errors.New("installation directory is outside the supplied root")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	current := "."
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		err := root.Mkdir(current, 0700)
		if errors.Is(err, os.ErrExist) {
			info, statErr := root.Lstat(current)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				return fmt.Errorf("installation ancestor %q is not a directory", current)
			}
			continue
		}
		if err != nil {
			return err
		}
		file, err := root.Open(current)
		if err != nil {
			return err
		}
		chmodErr := file.Chmod(0755)
		if err := errors.Join(chmodErr, file.Close()); err != nil {
			return err
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
