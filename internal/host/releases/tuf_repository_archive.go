package releases

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maxTUFRepositoryEntries      = 4096
	maxTUFRepositoryUncompressed = int64(2 << 30)
	maxTUFRepositoryArchiveBytes = int64(1 << 30)
)

func BuildTUFRepositoryArchive(
	ctx context.Context,
	root, output string,
	trustedRoot []byte,
	requirements []RepositoryRequirement,
) error {
	if err := VerifyTUFRepositoryDirectory(ctx, root, trustedRoot, requirements); err != nil {
		return err
	}
	output = strings.TrimSpace(output)
	if output == "" || !filepath.IsAbs(output) || filepath.Clean(output) != output ||
		output == string(filepath.Separator) || strings.ContainsRune(output, 0) {
		return errors.New("TUF repository archive output must be a clean absolute non-root path")
	}
	if _, err := os.Lstat(output); err == nil {
		return errors.New("TUF repository archive output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(output)
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("TUF repository archive parent must be a real directory")
	}

	paths, err := collectTUFRepositoryFiles(root)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(parent, ".tuf-repository-*.tar.gz")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempName)
		}
	}()
	if err = temp.Chmod(0644); err != nil {
		temp.Close()
		return err
	}
	gzipWriter, err := gzip.NewWriterLevel(temp, gzip.BestCompression)
	if err != nil {
		temp.Close()
		return err
	}
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)

	for _, relative := range paths {
		if err = ctx.Err(); err != nil {
			tarWriter.Close()
			gzipWriter.Close()
			temp.Close()
			return err
		}
		path := filepath.Join(root, filepath.FromSlash(relative))
		file, info, openErr := openRepositoryRegular(path)
		if openErr != nil {
			tarWriter.Close()
			gzipWriter.Close()
			temp.Close()
			return openErr
		}
		header := &tar.Header{
			Name:       relative,
			Mode:       0644,
			Size:       info.Size(),
			ModTime:    time.Unix(0, 0).UTC(),
			AccessTime: time.Unix(0, 0).UTC(),
			ChangeTime: time.Unix(0, 0).UTC(),
			Typeflag:   tar.TypeReg,
			Uid:        0,
			Gid:        0,
			Uname:      "",
			Gname:      "",
			Format:     tar.FormatPAX,
		}
		if err = tarWriter.WriteHeader(header); err == nil {
			_, err = io.CopyN(tarWriter, file, info.Size())
		}
		closeErr := file.Close()
		if err != nil {
			tarWriter.Close()
			gzipWriter.Close()
			temp.Close()
			return err
		}
		if closeErr != nil {
			tarWriter.Close()
			gzipWriter.Close()
			temp.Close()
			return closeErr
		}
	}
	if err = tarWriter.Close(); err != nil {
		gzipWriter.Close()
		temp.Close()
		return err
	}
	if err = gzipWriter.Close(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	info, err = os.Stat(tempName)
	if err != nil {
		return err
	}
	if info.Size() <= 0 || info.Size() > maxTUFRepositoryArchiveBytes {
		return errors.New("TUF repository archive exceeds size policy")
	}
	if err = unix.Renameat2(unix.AT_FDCWD, tempName, unix.AT_FDCWD, output, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errors.New("TUF repository archive output already exists")
		}
		return err
	}
	cleanup = false
	dir, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func VerifyTUFRepositoryArchive(
	ctx context.Context,
	archive string,
	trustedRoot []byte,
	requirements []RepositoryRequirement,
) error {
	temp, err := os.MkdirTemp("", "loki-tuf-archive-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err = ExtractTUFRepositoryArchive(archive, temp); err != nil {
		return err
	}
	return VerifyTUFRepositoryDirectory(ctx, temp, trustedRoot, requirements)
}

func ExtractTUFRepositoryArchive(archive, destination string) error {
	archive = strings.TrimSpace(archive)
	destination = strings.TrimSpace(destination)
	if archive == "" || !filepath.IsAbs(archive) || filepath.Clean(archive) != archive ||
		destination == "" || !filepath.IsAbs(destination) || filepath.Clean(destination) != destination {
		return errors.New("TUF repository archive paths must be clean absolute paths")
	}
	info, err := os.Lstat(destination)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("TUF repository extraction destination must be a real directory")
	}
	input, info, err := openRepositoryRegular(archive)
	if err != nil {
		return err
	}
	defer input.Close()
	if info.Size() <= 0 || info.Size() > maxTUFRepositoryArchiveBytes {
		return errors.New("TUF repository archive exceeds size policy")
	}
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return err
	}
	gzipReader.Multistream(false)
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	seen := map[string]bool{}
	var entries int
	var total int64
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		entries++
		if entries > maxTUFRepositoryEntries {
			return errors.New("TUF repository archive has too many entries")
		}
		if header.Typeflag != tar.TypeReg || header.Size <= 0 {
			return errors.New("TUF repository archive may contain only non-empty regular files")
		}
		relative, err := cleanArchiveRelativePath(header.Name)
		if err != nil {
			return err
		}
		if seen[relative] {
			return errors.New("TUF repository archive contains duplicate entries")
		}
		seen[relative] = true
		total += header.Size
		if total > maxTUFRepositoryUncompressed {
			return errors.New("TUF repository archive exceeds uncompressed size policy")
		}
		output := filepath.Join(destination, filepath.FromSlash(relative))
		if !pathWithin(destination, output) {
			return errors.New("TUF repository archive entry escapes the destination")
		}
		if err = os.MkdirAll(filepath.Dir(output), 0755); err != nil {
			return err
		}
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		written, copyErr := io.CopyN(file, tarReader, header.Size)
		syncErr := file.Sync()
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if written != header.Size {
			return io.ErrShortWrite
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if entries == 0 {
		return errors.New("TUF repository archive is empty")
	}
	return nil
}

func collectTUFRepositoryFiles(root string) ([]string, error) {
	var paths []string
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if _, err = cleanArchiveRelativePath(relative); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("TUF repository must not contain symlinks")
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return errors.New("TUF repository may contain only non-empty regular files")
		}
		paths = append(paths, relative)
		if len(paths) > maxTUFRepositoryEntries {
			return errors.New("TUF repository has too many files")
		}
		total += info.Size()
		if total > maxTUFRepositoryUncompressed {
			return errors.New("TUF repository exceeds uncompressed size policy")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("TUF repository is empty")
	}
	sort.Strings(paths)
	return paths, nil
}

func cleanArchiveRelativePath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) || strings.Contains(value, "\\") ||
		strings.HasPrefix(value, "/") || pathClean(value) != value ||
		value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return "", errors.New("TUF repository archive contains an unsafe path")
	}
	return value, nil
}

func pathClean(value string) string {
	return filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
