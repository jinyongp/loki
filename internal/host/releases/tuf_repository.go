package releases

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"golang.org/x/sys/unix"
)

const repositoryVerificationBaseURL = "https://loki-tuf.invalid/repository/"

var repositoryRootFilenamePattern = regexp.MustCompile(`^[1-9][0-9]*\.root\.json$`)

type RepositoryRequirement struct {
	Descriptor TargetDescriptor
}

type directoryRepositoryFetcher struct {
	root string
}

func VerifyTUFRepositoryDirectory(
	ctx context.Context,
	root string,
	trustedRoot []byte,
	requirements []RepositoryRequirement,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, err := validateRepositoryRoot(root)
	if err != nil {
		return err
	}
	bootstrap, err := parseRoot(trustedRoot)
	if err != nil {
		return fmt.Errorf("parse TUF repository trusted root: %w", err)
	}
	if err = validateRootPolicy(bootstrap); err != nil {
		return err
	}
	rootFile := filepath.Join(root, fmt.Sprintf("%d.root.json", bootstrap.Signed.Version))
	publishedRoot, err := readBoundedRegularFile(rootFile, maxRootMetadataBytes)
	if err != nil {
		return fmt.Errorf("read published TUF root: %w", err)
	}
	if !bytes.Equal(publishedRoot, trustedRoot) {
		return errors.New("published TUF root does not match the bootstrap trusted root bytes")
	}

	stateRoot, err := os.MkdirTemp("", "loki-tuf-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stateRoot)
	if err = os.Chmod(stateRoot, 0700); err != nil {
		return err
	}
	client, err := Open(Config{
		StateRoot:         stateRoot,
		RemoteMetadataURL: repositoryVerificationBaseURL,
		TrustedRoot:       trustedRoot,
		Fetcher:           directoryRepositoryFetcher{root: root},
	})
	if err != nil {
		return err
	}
	if err = client.Refresh(ctx); err != nil {
		return fmt.Errorf("refresh signed TUF repository: %w", err)
	}

	seen := map[string]bool{}
	for _, requirement := range requirements {
		descriptor := requirement.Descriptor
		if err = validateTargetDescriptor(descriptor, targetNamespace(descriptor.Path)); err != nil {
			return fmt.Errorf("required repository target %q is invalid: %w", descriptor.Path, err)
		}
		if seen[descriptor.Path] {
			return errors.New("required TUF repository targets must be unique")
		}
		seen[descriptor.Path] = true
		namespace := targetNamespace(descriptor.Path)
		relative := strings.TrimPrefix(descriptor.Path, namespace+"/")
		var actual TargetDescriptor
		var raw []byte
		switch namespace {
		case "releases":
			actual, raw, err = client.FetchRelease(ctx, relative)
		case "toolchains":
			actual, raw, err = client.FetchToolchain(ctx, relative)
		default:
			err = errors.New("required TUF repository target namespace is unsupported")
		}
		if err != nil {
			return fmt.Errorf("fetch required TUF target %s: %w", descriptor.Path, err)
		}
		if actual != descriptor {
			return fmt.Errorf("TUF repository target %s does not match the accepted descriptor", descriptor.Path)
		}
		if err = descriptor.VerifyBytes(raw); err != nil {
			return fmt.Errorf("verify required TUF target %s: %w", descriptor.Path, err)
		}
	}
	return nil
}

func TrustedTUFRootForDigest(root, expectedSHA256 string) ([]byte, error) {
	root, err := validateRepositoryRoot(root)
	if err != nil {
		return nil, err
	}
	expectedSHA256 = strings.TrimSpace(expectedSHA256)
	if len(expectedSHA256) != sha256.Size*2 {
		return nil, errors.New("trusted TUF root digest is invalid")
	}
	expected, err := hex.DecodeString(expectedSHA256)
	if err != nil || len(expected) != sha256.Size {
		return nil, errors.New("trusted TUF root digest is invalid")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var matched []byte
	for _, entry := range entries {
		if entry.IsDir() || !repositoryRootFilenamePattern.MatchString(entry.Name()) {
			continue
		}
		raw, readErr := readBoundedRegularFile(filepath.Join(root, entry.Name()), maxRootMetadataBytes)
		if readErr != nil {
			return nil, readErr
		}
		sum := sha256.Sum256(raw)
		if !bytes.Equal(sum[:], expected) {
			continue
		}
		parsed, parseErr := parseRoot(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("trusted TUF root matching bootstrap digest is invalid: %w", parseErr)
		}
		if parseErr = validateRootPolicy(parsed); parseErr != nil {
			return nil, parseErr
		}
		expectedName := fmt.Sprintf("%d.root.json", parsed.Signed.Version)
		if entry.Name() != expectedName {
			return nil, errors.New("trusted TUF root filename does not match metadata version")
		}
		if matched != nil {
			return nil, errors.New("TUF repository contains duplicate bootstrap trusted-root bytes")
		}
		matched = append([]byte(nil), raw...)
	}
	if matched == nil {
		return nil, errors.New("TUF repository does not contain the bootstrap trusted root")
	}
	return matched, nil
}

func targetNamespace(targetPath string) string {
	switch {
	case strings.HasPrefix(targetPath, "releases/"):
		return "releases"
	case strings.HasPrefix(targetPath, "toolchains/"):
		return "toolchains"
	default:
		return ""
	}
}

func validateRepositoryRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
		return "", errors.New("TUF repository root must be a clean absolute non-root path")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if resolved != root {
		return "", errors.New("TUF repository root must not traverse symlinks")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("TUF repository root must be a real directory")
	}
	return root, nil
}

func (f directoryRepositoryFetcher) DownloadFile(urlPath string, maxLength int64, _ time.Duration) ([]byte, error) {
	parsed, err := url.Parse(urlPath)
	if err != nil {
		return nil, err
	}
	base, _ := url.Parse(repositoryVerificationBaseURL)
	if parsed.Scheme != base.Scheme || parsed.Host != base.Host ||
		!strings.HasPrefix(parsed.Path, base.Path) || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusNotFound, URL: urlPath}
	}
	relative := strings.TrimPrefix(parsed.Path, base.Path)
	if relative == "" || strings.HasPrefix(relative, "/") || filepath.ToSlash(filepath.Clean(relative)) != relative ||
		strings.HasPrefix(relative, "../") {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusNotFound, URL: urlPath}
	}
	path := filepath.Join(f.root, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusNotFound, URL: urlPath}
	}
	if err != nil {
		return nil, err
	}
	if !pathWithin(f.root, resolved) {
		return nil, errors.New("TUF repository path escapes the repository root")
	}
	file, info, err := openRepositoryRegular(resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil, &metadata.ErrDownloadHTTP{StatusCode: http.StatusNotFound, URL: urlPath}
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if maxLength > 0 && info.Size() > maxLength {
		return nil, errors.New("TUF repository file exceeds requested size limit")
	}
	limit := info.Size()
	if maxLength > 0 && maxLength < limit {
		limit = maxLength
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != info.Size() {
		return nil, errors.New("TUF repository file changed while being read")
	}
	return raw, nil
}

func openRepositoryRegular(path string) (*os.File, os.FileInfo, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		file.Close()
		return nil, nil, errors.New("TUF repository entry must be a regular file")
	}
	return file, info, nil
}
