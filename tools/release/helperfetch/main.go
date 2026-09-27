package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/host/connect"
)

const maxCatalogBytes = int64(4 << 20)

type options struct {
	Catalog string
	Output  string
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func main() {
	var cfg options
	flag.StringVar(&cfg.Catalog, "catalog", "", "connect helper catalog path")
	flag.StringVar(&cfg.Output, "output", "", "absolute output directory")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
	if err := fetchHelpers(context.Background(), cfg, client); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fetchHelpers(ctx context.Context, cfg options, client httpDoer) error {
	catalogPath, err := cleanAbsolutePath(cfg.Catalog, "catalog")
	if err != nil {
		return err
	}
	output, err := cleanAbsolutePath(cfg.Output, "output")
	if err != nil {
		return err
	}
	if _, err = os.Lstat(output); err == nil {
		return errors.New("connect helper output already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(output)
	if info, statErr := os.Lstat(parent); statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("connect helper output parent must be a real directory")
	}
	raw, err := readBoundedRegular(catalogPath, maxCatalogBytes)
	if err != nil {
		return err
	}
	catalog, err := connect.LoadCatalog(raw)
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp(parent, ".loki-connect-helpers-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	if err = os.Chmod(temp, 0o755); err != nil {
		return err
	}
	for _, helper := range catalog.Helpers {
		for _, asset := range helper.Assets() {
			if err = fetchAsset(ctx, client, temp, asset); err != nil {
				return fmt.Errorf("%s/%s: %w", helper.ID, helper.Platform, err)
			}
		}
	}
	if err = syncDirectory(temp); err != nil {
		return err
	}
	if err = unix.Renameat2(unix.AT_FDCWD, temp, unix.AT_FDCWD, output, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errors.New("connect helper output already exists")
		}
		return err
	}
	return syncDirectory(parent)
}

func fetchAsset(ctx context.Context, client httpDoer, root string, asset connect.Asset) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.SourceURL, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != asset.SourceLength {
		return errors.New("download Content-Length does not match catalog")
	}
	path := filepath.Join(root, asset.MirrorAsset)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, asset.SourceLength+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != asset.SourceLength || hex.EncodeToString(digest.Sum(nil)) != asset.SourceSHA256 {
		_ = os.Remove(path)
		return errors.New("download bytes do not match catalog identity")
	}
	return nil
}

func cleanAbsolutePath(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) ||
		strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s must be a clean absolute non-root path", label)
	}
	return value, nil
}

func readBoundedRegular(path string, limit int64) ([]byte, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !linkInfo.Mode().IsRegular() || linkInfo.Mode()&os.ModeSymlink != 0 ||
		linkInfo.Size() <= 0 || linkInfo.Size() > limit {
		return nil, errors.New("catalog must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != linkInfo.Size() {
		return nil, errors.New("catalog changed while being opened")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != info.Size() {
		return nil, errors.New("catalog changed while being read")
	}
	return raw, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
