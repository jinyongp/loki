package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/policy"
	"loki/internal/workspace"
)

type stagedBrowserUpload struct {
	Tokens     []string
	Files      []map[string]any
	TotalBytes int64
	inbox      string
}

func (s *stagedBrowserUpload) Cleanup() {
	if s == nil || s.inbox == "" {
		return
	}
	for _, token := range s.Tokens {
		_ = os.Remove(filepath.Join(s.inbox, token))
	}
}

type BrowserUploadStager interface {
	StageBrowserUpload(context.Context, *workspace.Files, []string) (*stagedBrowserUpload, error)
}

func browserUploadPaths(args map[string]any) ([]string, error) {
	raw, ok := args["paths"]
	if !ok || raw == nil {
		return nil, fault.Error("browser upload paths are required")
	}
	paths := []string{}
	switch typed := raw.(type) {
	case []string:
		paths = append(paths, typed...)
	case []any:
		paths = make([]string, 0, len(typed))
		for _, item := range typed {
			path, ok := item.(string)
			if !ok {
				return nil, fault.Error("browser upload paths must contain only strings")
			}
			paths = append(paths, path)
		}
	default:
		return nil, fault.Error("browser upload paths must be an array")
	}
	if len(paths) == 0 {
		return nil, fault.Error("browser upload paths are required")
	}
	return paths, nil
}

func browserUploadReferences(stage *stagedBrowserUpload) []any {
	if stage == nil {
		return nil
	}
	result := make([]any, 0, len(stage.Tokens))
	for index, token := range stage.Tokens {
		name, _ := stage.Files[index]["name"].(string)
		result = append(result, map[string]any{"token": token, "name": name})
	}
	return result
}

func uploadToken() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func copyUpload(ctx context.Context, destination *os.File, source *os.File, limit int64) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return total, errors.New("browser upload byte limit exceeded")
			}
			written := 0
			for written < n {
				count, writeErr := destination.Write(buffer[written:n])
				if writeErr != nil {
					return total, writeErr
				}
				if count == 0 {
					return total, io.ErrShortWrite
				}
				written += count
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func (c BrowserRPC) StageBrowserUpload(ctx context.Context, files *workspace.Files, paths []string) (_ *stagedBrowserUpload, err error) {
	if files == nil || files.Policy == nil {
		return nil, fault.Error("workspace file access is unavailable")
	}
	if len(paths) == 0 || len(paths) > files.Config.BrowserMaxUploadFiles {
		return nil, fault.Error("browser upload file-count limit exceeded")
	}
	if _, err = os.Stat(c.Client.Socket); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fault.Error("browser is not configured")
		}
		return nil, err
	}
	inbox := filepath.Join(filepath.Dir(c.Client.Socket), "uploads")
	directory, err := unix.Open(inbox, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fault.Error("browser upload inbox is unavailable")
	}
	defer unix.Close(directory)

	stage := &stagedBrowserUpload{inbox: inbox}
	defer func() {
		if err != nil {
			stage.Cleanup()
		}
	}()
	seenPaths := map[string]bool{}
	for _, requested := range paths {
		relative, relativeErr := policy.Relative(requested)
		if relativeErr != nil {
			return nil, relativeErr
		}
		if seenPaths[relative] {
			return nil, fault.Error("browser upload paths must be unique")
		}
		seenPaths[relative] = true
		source, openErr := files.Policy.Open(relative, os.O_RDONLY, 0)
		if openErr != nil {
			return nil, openErr
		}
		info, statErr := source.Stat()
		if statErr != nil {
			source.Close()
			return nil, statErr
		}
		if !info.Mode().IsRegular() {
			source.Close()
			return nil, fault.Error("browser upload path must be a regular file")
		}
		remaining := int64(files.Config.BrowserMaxUploadBytes) - stage.TotalBytes
		if remaining < 0 || info.Size() > remaining {
			source.Close()
			return nil, fault.Error("browser upload byte limit exceeded")
		}
		token, tokenErr := uploadToken()
		if tokenErr != nil {
			source.Close()
			return nil, tokenErr
		}
		fd, createErr := unix.Openat2(directory, token, &unix.OpenHow{
			Flags:   unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Mode:    0o640,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		if createErr != nil {
			source.Close()
			return nil, createErr
		}
		destination := os.NewFile(uintptr(fd), token)
		copied, copyErr := copyUpload(ctx, destination, source, remaining)
		sourceErr := source.Close()
		syncErr := destination.Sync()
		chmodErr := destination.Chmod(0o640)
		closeErr := destination.Close()
		if copyErr != nil {
			return nil, copyErr
		}
		if sourceErr != nil {
			return nil, sourceErr
		}
		if syncErr != nil {
			return nil, syncErr
		}
		if chmodErr != nil {
			return nil, chmodErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		stage.Tokens = append(stage.Tokens, token)
		stage.TotalBytes += copied
		stage.Files = append(stage.Files, map[string]any{
			"path":  relative,
			"name":  filepath.Base(relative),
			"bytes": copied,
		})
	}
	return stage, nil
}
