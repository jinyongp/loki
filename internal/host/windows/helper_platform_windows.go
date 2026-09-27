//go:build windows

package windows

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type HTTPHelperDownloader struct {
	Client *http.Client
}

func (downloader HTTPHelperDownloader) Fetch(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > maxRuntimeHelperBytes+1 {
		return nil, errors.New("helper download bound is invalid")
	}
	client := downloader.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("helper mirror returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return nil, errors.New("helper mirror response exceeds download bound")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, errors.New("helper mirror response exceeds download bound")
	}
	return raw, nil
}

type WindowsHelperInstallPlatform struct{}

func (WindowsHelperInstallPlatform) Lstat(path string) (StatePath, error) {
	return OSStateFilesystem{}.Lstat(path)
}

func (WindowsHelperInstallPlatform) ReadFile(path string) ([]byte, error) {
	info, err := OSStateFilesystem{}.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Exists || !info.Regular || info.Reparse {
		return nil, errors.New("helper metadata is not a regular non-reparse file")
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fileInfo.Size() <= 0 || fileInfo.Size() > maxRuntimeCatalogBytes {
		return nil, errors.New("helper metadata size is invalid")
	}
	return os.ReadFile(path)
}

func (WindowsHelperInstallPlatform) FileDigest(path string) (string, int64, error) {
	return (WindowsFrontendPlatform{}).FileDigest(path)
}

func (WindowsHelperInstallPlatform) ListDirectory(path string) ([]string, error) {
	info, err := OSStateFilesystem{}.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Exists || !info.Directory || info.Reparse {
		return nil, errors.New("helper path is not a real directory")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func (WindowsHelperInstallPlatform) EnsurePrivateDirectory(ctx context.Context, target string) error {
	if err := validateExistingDirectoryPrefixes(filepath.Dir(target)); err != nil {
		return err
	}
	info, err := OSStateFilesystem{}.Lstat(target)
	if err != nil {
		return err
	}
	if info.Exists {
		if !info.Directory || info.Reparse {
			return errors.New("managed helper path is not a real directory")
		}
		return verifyPrivateACL(target, true)
	}
	if err = os.Mkdir(target, 0o700); err != nil {
		return err
	}
	if err = applyPrivateACL(ctx, target, true); err != nil {
		_ = os.Remove(target)
		return err
	}
	return verifyPrivateACL(target, true)
}

func (WindowsHelperInstallPlatform) VerifyPrivatePath(target string, directory bool) error {
	info, err := OSStateFilesystem{}.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Exists || info.Reparse || directory && !info.Directory || !directory && !info.Regular {
		return errors.New("managed helper path type or reparse identity changed")
	}
	return verifyPrivateACL(target, directory)
}

func (WindowsHelperInstallPlatform) CreatePrivateTempDirectory(ctx context.Context, parent, prefix string) (string, error) {
	if err := (WindowsHelperInstallPlatform{}).VerifyPrivatePath(parent, true); err != nil {
		return "", err
	}
	if strings.ContainsAny(prefix, "\\/\x00") || !strings.HasPrefix(prefix, ".loki-helper-") {
		return "", errors.New("helper staging prefix is invalid")
	}
	target, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", err
	}
	if err = applyPrivateACL(ctx, target, true); err != nil {
		_ = os.Remove(target)
		return "", err
	}
	return target, nil
}

func (WindowsHelperInstallPlatform) WritePrivateFile(ctx context.Context, target string, raw []byte) error {
	parent := filepath.Dir(target)
	if err := (WindowsHelperInstallPlatform{}).VerifyPrivatePath(parent, true); err != nil {
		return err
	}
	if len(raw) == 0 || int64(len(raw)) > maxRuntimeHelperBytes {
		return errors.New("managed helper file size is invalid")
	}
	info, err := OSStateFilesystem{}.Lstat(target)
	if err != nil {
		return err
	}
	if info.Exists {
		return errors.New("managed helper staging file already exists")
	}
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	closed := false
	cleanup := true
	defer func() {
		if !closed {
			_ = file.Close()
		}
		if cleanup {
			_ = os.Remove(target)
		}
	}()
	if _, err = file.Write(raw); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	closed = true
	if err = applyPrivateACL(ctx, target, false); err != nil {
		return err
	}
	if err = verifyPrivateACL(target, false); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (WindowsHelperInstallPlatform) PublishDirectory(staging, target string) error {
	if err := (WindowsHelperInstallPlatform{}).VerifyPrivatePath(staging, true); err != nil {
		return err
	}
	if err := (WindowsHelperInstallPlatform{}).VerifyPrivatePath(filepath.Dir(target), true); err != nil {
		return err
	}
	info, err := OSStateFilesystem{}.Lstat(target)
	if err != nil {
		return err
	}
	if info.Exists {
		return errors.New("managed helper target already exists")
	}
	if err = os.Rename(staging, target); err != nil {
		return err
	}
	return verifyPrivateACL(target, true)
}

func (WindowsHelperInstallPlatform) RemoveTree(target string) error {
	if !strings.HasPrefix(filepath.Base(target), ".loki-helper-") {
		return errors.New("refusing to remove non-staging helper directory")
	}
	info, err := OSStateFilesystem{}.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Exists {
		return nil
	}
	if !info.Directory || info.Reparse {
		return errors.New("refusing to remove unsafe helper staging path")
	}
	return os.RemoveAll(target)
}

func NewWindowsHelperManager(binding ReleaseBinding, paths FrontendPaths) HelperManager {
	return HelperManager{
		Platform:    WindowsHelperInstallPlatform{},
		Downloader:  HTTPHelperDownloader{},
		Binding:     binding,
		HelpersRoot: paths.HelpersRoot,
	}
}
