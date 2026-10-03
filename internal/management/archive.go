package management

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"loki/internal/tools"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

const maxExtractedBytes int64 = 8 << 30
const maxArchiveEntries = 100000

func archivePath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid archive path %q", name)
	}
	name = strings.TrimSuffix(name, "/")
	if path.Clean(name) != name || name == "." || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("noncanonical archive path %q", name)
	}
	return name, nil
}

func extract(archive, format, destination string) error {
	return extractWithBrowserLinks(archive, format, destination, false)
}

func extractArtifact(archive string, artifact tools.Artifact, destination string) error {
	allowLinks := runtime.GOOS == "darwin" && artifact.Module == "browser" && artifact.Target.OS == "darwin" && artifact.Format == "zip"
	return extractWithBrowserLinks(archive, artifact.Format, destination, allowLinks)
}

// Native macOS Chrome uses signed framework links. This exception is limited
// to relative links within one Chrome app; every other archive keeps the
// regular-file/directory contract. Files are written before any links exist.
func extractWithBrowserLinks(archive, format, destination string, allowBrowserLinks bool) error {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	var total int64
	count := 0
	entries := make(map[string]bool)
	links := make(map[string]string)
	write := func(name string, size int64, mode os.FileMode, directory bool, source io.Reader) error {
		name, err := archivePath(name)
		if err != nil {
			return err
		}
		count++
		entries[name] = true
		if count > maxArchiveEntries || size < 0 || size > maxExtractedBytes-total {
			return fmt.Errorf("artifact extraction limit exceeded")
		}
		if directory {
			if size != 0 {
				return fmt.Errorf("archive directory contains unexpected payload")
			}
			return root.MkdirAll(name, 0755)
		}
		total += size
		if err := root.MkdirAll(path.Dir(name), 0755); err != nil {
			return err
		}
		permission := os.FileMode(0644)
		if mode&0111 != 0 {
			permission = 0755
		}
		f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permission)
		if err != nil {
			return err
		}
		n, copyErr := io.CopyN(f, source, size)
		if copyErr == nil {
			extra, tailErr := io.Copy(io.Discard, io.LimitReader(source, 1))
			if tailErr != nil {
				copyErr = tailErr
			} else if extra != 0 {
				copyErr = fmt.Errorf("archive member exceeds declared size")
			}
		}
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if n != size {
			return fmt.Errorf("short archive member")
		}
		return closeErr
	}
	if format == "zip" {
		z, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if f.Mode()&os.ModeSymlink != 0 && allowBrowserLinks {
				name, err := archivePath(f.Name)
				if err != nil {
					return err
				}
				if entries[name] || f.UncompressedSize64 == 0 || f.UncompressedSize64 > 4096 {
					return fmt.Errorf("invalid browser framework link %q", name)
				}
				count++
				total += int64(f.UncompressedSize64)
				if count > maxArchiveEntries || total > maxExtractedBytes {
					return fmt.Errorf("artifact extraction limit exceeded")
				}
				r, err := f.Open()
				if err != nil {
					return err
				}
				data, readErr := io.ReadAll(io.LimitReader(r, 4097))
				closeErr := r.Close()
				if readErr != nil {
					return readErr
				}
				if closeErr != nil {
					return closeErr
				}
				if uint64(len(data)) != f.UncompressedSize64 {
					return fmt.Errorf("browser framework link size differs from header")
				}
				if _, err := browserLinkScope(name, string(data)); err != nil {
					return err
				}
				entries[name] = true
				links[name] = string(data)
				continue
			}
			if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
				return fmt.Errorf("artifact contains special file %q", f.Name)
			}
			if f.UncompressedSize64 > uint64(maxExtractedBytes) {
				return fmt.Errorf("artifact member too large")
			}
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = write(f.Name, int64(f.UncompressedSize64), f.Mode(), f.FileInfo().IsDir(), r)
			closeErr := r.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return createBrowserLinks(root, destination, entries, links)
	}
	if format != "tar.gz" {
		return fmt.Errorf("unsupported archive format %q", format)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	g, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer g.Close()
	t := tar.NewReader(g)
	for {
		h, err := t.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return fmt.Errorf("artifact contains special file %q", h.Name)
		}
		if err := write(h.Name, h.Size, os.FileMode(h.Mode), h.Typeflag == tar.TypeDir, t); err != nil {
			return err
		}
	}
}

func browserLinkScope(name, target string) (string, error) {
	index := strings.Index(name, ".app/Contents/")
	if !strings.HasPrefix(name, "chrome/") || index < len("chrome/") || target == "" || strings.HasPrefix(target, "/") || strings.ContainsAny(target, "\\:\x00") {
		return "", fmt.Errorf("invalid browser framework link %q", name)
	}
	scope := name[:index+len(".app")]
	resolved := path.Clean(path.Join(path.Dir(name), target))
	if resolved != scope && !strings.HasPrefix(resolved, scope+"/") {
		return "", fmt.Errorf("browser framework link escapes its app: %q", name)
	}
	return scope, nil
}

func createBrowserLinks(root *os.Root, destination string, entries map[string]bool, links map[string]string) error {
	// No archive member may be written beneath a link alias, irrespective of
	// entry order. This also rules out using an app link as a parent of a link.
	for entry := range entries {
		for parent := path.Dir(entry); parent != "."; parent = path.Dir(parent) {
			if _, exists := links[parent]; exists {
				return fmt.Errorf("archive member traverses browser framework link: %q", entry)
			}
		}
	}
	for name, target := range links {
		if err := root.MkdirAll(path.Dir(name), 0755); err != nil {
			return err
		}
		if err := root.Symlink(target, name); err != nil {
			return err
		}
	}
	realRoot, err := filepath.EvalSymlinks(destination)
	if err != nil {
		return err
	}
	for name, target := range links {
		scope, err := browserLinkScope(name, target)
		if err != nil {
			return err
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(realRoot, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("browser framework link is dangling or cyclic: %q: %w", name, err)
		}
		relative, err := filepath.Rel(realRoot, resolved)
		if err != nil || !filepath.IsLocal(relative) || !strings.HasPrefix(filepath.ToSlash(relative), scope+"/") {
			return fmt.Errorf("browser framework link chain escapes its app: %q", name)
		}
		info, err := os.Stat(resolved)
		if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
			return fmt.Errorf("browser framework link has invalid target: %q", name)
		}
	}
	return nil
}
