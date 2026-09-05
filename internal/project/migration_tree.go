package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
)

// Legacy migration reads only pinned regular files/directories. Its manifest
// includes empty directories and file digests so a second pass detects changes.
func migrationTree(ctx context.Context, source, destination string) (map[string]string, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, source, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), "legacy tree")
	defer root.Close()
	manifest := map[string]string{}
	count := 0
	var total int64
	var walk func(*os.File, string, int) error
	walk = func(directory *os.File, relative string, depth int) error {
		if depth > 32 {
			return fault.Error("legacy migration directory depth exceeds limit")
		}
		manifest[relative] = "directory"
		if destination != "" {
			if err := os.Mkdir(filepath.Join(destination, relative), 0700); err != nil {
				return err
			}
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			entries, err := directory.ReadDir(128)
			if err != nil && err != io.EOF {
				return err
			}
			for _, entry := range entries {
				count++
				if count > 10000 {
					return fault.Error("legacy migration entry count exceeds limit")
				}
				if err := func() error {
					fd, err := unix.Openat(int(directory.Fd()), entry.Name(), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
					if err != nil {
						return err
					}
					file := os.NewFile(uintptr(fd), "legacy entry")
					defer file.Close()
					info, err := file.Stat()
					if err != nil {
						return err
					}
					name := filepath.Join(relative, entry.Name())
					if info.IsDir() {
						return walk(file, name, depth+1)
					}
					if !info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink != 1 {
						return fault.Error("legacy migration contains a link or special file")
					}
					if info.Size() > 256*1024*1024 || total+info.Size() > 1024*1024*1024 {
						return fault.Error("legacy migration data exceeds limit")
					}
					digest := sha256.New()
					var writer io.Writer = digest
					var output *os.File
					if destination != "" {
						output, err = os.OpenFile(filepath.Join(destination, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
						if err != nil {
							return err
						}
						defer output.Close()
						writer = io.MultiWriter(digest, output)
					}
					written, err := io.Copy(writer, io.LimitReader(migrationReader{ctx, file}, info.Size()+1))
					if err != nil {
						return err
					}
					after, err := file.Stat()
					if err != nil {
						return err
					}
					if written != info.Size() || !info.ModTime().Equal(after.ModTime()) || after.Size() != info.Size() {
						return fault.Error("legacy migration source changed during copy")
					}
					if output != nil {
						if err = output.Sync(); err != nil {
							return err
						}
					}
					total += written
					manifest[name] = hex.EncodeToString(digest.Sum(nil))
					return nil
				}(); err != nil {
					return err
				}
			}
			if err == io.EOF {
				break
			}
		}
		if destination != "" {
			dir, err := os.Open(filepath.Join(destination, relative))
			if err != nil {
				return err
			}
			defer dir.Close()
			return dir.Sync()
		}
		return nil
	}
	if err = walk(root, ".", 0); err != nil {
		return nil, err
	}
	return manifest, nil
}

type migrationReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r migrationReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}
