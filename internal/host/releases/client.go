package releases

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/config"
	"github.com/theupdateframework/go-tuf/v2/metadata/fetcher"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/updater"
	"golang.org/x/sys/unix"
)

const (
	maxRootMetadataBytes      = int64(512 << 10)
	maxTimestampMetadataBytes = int64(64 << 10)
	maxSnapshotMetadataBytes  = int64(8 << 20)
	maxTargetsMetadataBytes   = int64(16 << 20)
	maxTargetBytes            = int64(1 << 30)
	maxRootRotations          = int64(256)
	maxDelegations            = 32
)

type Config struct {
	StateRoot         string
	RemoteMetadataURL string
	TrustedRoot       []byte
	Fetcher           fetcher.Fetcher
}

type TargetDescriptor struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
	SHA256 string `json:"sha256"`
}

func (d TargetDescriptor) VerifyBytes(raw []byte) error {
	if d.Length <= 0 || d.Length > maxTargetBytes || int64(len(raw)) != d.Length {
		return errors.New("target length does not match verified metadata")
	}
	expected, err := hex.DecodeString(d.SHA256)
	if err != nil || len(expected) != sha256.Size {
		return errors.New("target SHA-256 identity is invalid")
	}
	actual := sha256.Sum256(raw)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return errors.New("target SHA-256 does not match verified metadata")
	}
	return nil
}

type Client struct {
	metadataDir       string
	targetsDir        string
	lockPath          string
	remoteMetadataURL string
	bootstrapRoot     []byte
	bootstrapVersion  int64
	fetcher           fetcher.Fetcher
	mu                sync.Mutex
}

func Open(cfg Config) (*Client, error) {
	root, err := validateStateRoot(cfg.StateRoot)
	if err != nil {
		return nil, err
	}
	remote, err := validateRemoteMetadataURL(cfg.RemoteMetadataURL)
	if err != nil {
		return nil, err
	}
	bootstrap, err := parseRoot(cfg.TrustedRoot)
	if err != nil {
		return nil, fmt.Errorf("trusted bootstrap root is invalid: %w", err)
	}
	if err = validateRootPolicy(bootstrap); err != nil {
		return nil, err
	}
	rootRole := bootstrap.Signed.Roles[metadata.ROOT]
	if rootRole.Threshold != 2 || len(rootRole.KeyIDs) != 3 {
		return nil, errors.New("trusted bootstrap root must use the initial 2-of-3 root policy")
	}

	metadataDir := filepath.Join(root, "metadata")
	targetsDir := filepath.Join(root, "targets")
	for _, dir := range []string{metadataDir, targetsDir} {
		if err = ensurePrivateDirectory(dir); err != nil {
			return nil, err
		}
	}
	return &Client{
		metadataDir:       metadataDir,
		targetsDir:        targetsDir,
		lockPath:          filepath.Join(root, "metadata.lock"),
		remoteMetadataURL: remote,
		bootstrapRoot:     bytes.Clone(cfg.TrustedRoot),
		bootstrapVersion:  bootstrap.Signed.Version,
		fetcher:           cfg.Fetcher,
	}, nil
}

func (c *Client) Refresh(ctx context.Context) error {
	if c == nil {
		return errors.New("release metadata client is not configured")
	}
	return c.withUpdater(ctx, func(*updater.Updater) error { return nil })
}

func (c *Client) ResolveRelease(ctx context.Context, relativePath string) (TargetDescriptor, error) {
	return c.resolve(ctx, "releases", relativePath)
}

func (c *Client) ResolveToolchain(ctx context.Context, relativePath string) (TargetDescriptor, error) {
	return c.resolve(ctx, "toolchains", relativePath)
}

func (c *Client) FetchRelease(ctx context.Context, relativePath string) (TargetDescriptor, []byte, error) {
	return c.fetch(ctx, "releases", relativePath)
}

func (c *Client) FetchToolchain(ctx context.Context, relativePath string) (TargetDescriptor, []byte, error) {
	return c.fetch(ctx, "toolchains", relativePath)
}

func (c *Client) resolve(ctx context.Context, namespace, relativePath string) (TargetDescriptor, error) {
	if c == nil {
		return TargetDescriptor{}, errors.New("release metadata client is not configured")
	}
	targetPath, err := namespacedTargetPath(namespace, relativePath)
	if err != nil {
		return TargetDescriptor{}, err
	}
	var descriptor TargetDescriptor
	err = c.withUpdater(ctx, func(update *updater.Updater) error {
		info, resolveErr := update.GetTargetInfo(targetPath)
		if resolveErr != nil {
			return resolveErr
		}
		descriptor, resolveErr = descriptorFromTargetInfo(targetPath, info)
		return resolveErr
	})
	if err != nil {
		return TargetDescriptor{}, err
	}
	return descriptor, nil
}

func (c *Client) fetch(ctx context.Context, namespace, relativePath string) (TargetDescriptor, []byte, error) {
	if c == nil {
		return TargetDescriptor{}, nil, errors.New("release metadata client is not configured")
	}
	targetPath, err := namespacedTargetPath(namespace, relativePath)
	if err != nil {
		return TargetDescriptor{}, nil, err
	}
	var descriptor TargetDescriptor
	var raw []byte
	err = c.withUpdater(ctx, func(update *updater.Updater) error {
		info, fetchErr := update.GetTargetInfo(targetPath)
		if fetchErr != nil {
			return fetchErr
		}
		descriptor, fetchErr = descriptorFromTargetInfo(targetPath, info)
		if fetchErr != nil {
			return fetchErr
		}
		_, cached, fetchErr := update.FindCachedTarget(info, "")
		if fetchErr != nil {
			return fetchErr
		}
		if cached != nil {
			raw = append([]byte(nil), cached...)
			return nil
		}
		_, downloaded, fetchErr := update.DownloadTarget(info, "", "")
		if fetchErr != nil {
			return fetchErr
		}
		raw = append([]byte(nil), downloaded...)
		return descriptor.VerifyBytes(raw)
	})
	if err != nil {
		return TargetDescriptor{}, nil, err
	}
	return descriptor, raw, nil
}

func descriptorFromTargetInfo(targetPath string, info *metadata.TargetFiles) (TargetDescriptor, error) {
	if info == nil || info.Path != targetPath {
		return TargetDescriptor{}, errors.New("verified target metadata is incomplete")
	}
	if info.Length <= 0 || info.Length > maxTargetBytes {
		return TargetDescriptor{}, errors.New("verified target length exceeds policy")
	}
	sum, ok := info.Hashes["sha256"]
	if !ok || len(sum) != sha256.Size {
		return TargetDescriptor{}, errors.New("verified target is missing a SHA-256 identity")
	}
	return TargetDescriptor{
		Path:   info.Path,
		Length: info.Length,
		SHA256: hex.EncodeToString(sum),
	}, nil
}

func (c *Client) withUpdater(ctx context.Context, fn func(*updater.Updater) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	lock, err := c.lock()
	if err != nil {
		return err
	}
	defer unlock(lock)

	trustedRoot, err := c.trustedRoot()
	if err != nil {
		return err
	}
	cfg, err := config.New(c.remoteMetadataURL, trustedRoot)
	if err != nil {
		return err
	}
	cfg.LocalMetadataDir = c.metadataDir
	cfg.LocalTargetsDir = c.targetsDir
	cfg.RootMaxLength = maxRootMetadataBytes
	cfg.TimestampMaxLength = maxTimestampMetadataBytes
	cfg.SnapshotMaxLength = maxSnapshotMetadataBytes
	cfg.TargetsMaxLength = maxTargetsMetadataBytes
	cfg.MaxRootRotations = maxRootRotations
	cfg.MaxDelegations = maxDelegations
	cfg.PrefixTargetsWithHash = true
	cfg.DisableLocalCache = false
	cfg.UnsafeLocalMode = false
	if c.fetcher != nil {
		cfg.Fetcher = c.fetcher
	}
	update, err := updater.New(cfg)
	if err != nil {
		return err
	}
	if err = update.Refresh(); err != nil {
		return err
	}
	if err = validateTrustedMetadata(update.GetTrustedMetadataSet()); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = fn(update); err != nil {
		return err
	}
	return ctx.Err()
}

func (c *Client) trustedRoot() ([]byte, error) {
	cached, err := readBoundedRegularFile(filepath.Join(c.metadataDir, "root.json"), maxRootMetadataBytes)
	if errors.Is(err, os.ErrNotExist) {
		return bytes.Clone(c.bootstrapRoot), nil
	}
	if err != nil {
		return nil, err
	}
	root, err := parseRoot(cached)
	if err != nil {
		return nil, fmt.Errorf("cached trusted root is invalid: %w", err)
	}
	if err = validateRootPolicy(root); err != nil {
		return nil, err
	}
	if root.Signed.Version <= c.bootstrapVersion {
		return bytes.Clone(c.bootstrapRoot), nil
	}
	return cached, nil
}

func validateTrustedMetadata(trusted trustedmetadata.TrustedMetadata) error {
	if trusted.Root == nil || trusted.Timestamp == nil || trusted.Snapshot == nil || trusted.Targets[metadata.TARGETS] == nil {
		return errors.New("trusted update metadata is incomplete")
	}
	if err := validateRootPolicy(trusted.Root); err != nil {
		return err
	}
	targets := trusted.Targets[metadata.TARGETS]
	if len(targets.Signed.Targets) != 0 {
		return errors.New("top-level targets must delegate Loki release and toolchain targets")
	}
	if err := validateDelegations(targets.Signed.Delegations); err != nil {
		return err
	}
	for _, name := range []string{"targets.json", "releases.json", "toolchains.json"} {
		meta, ok := trusted.Snapshot.Signed.Meta[name]
		if !ok || meta == nil || meta.Version < 1 {
			return fmt.Errorf("snapshot is missing %s", name)
		}
	}
	snapshot, ok := trusted.Timestamp.Signed.Meta["snapshot.json"]
	if !ok || snapshot == nil || snapshot.Version < 1 {
		return errors.New("timestamp is missing snapshot metadata")
	}
	return nil
}

func validateRootPolicy(root *metadata.Metadata[metadata.RootType]) error {
	if root == nil || root.Signed.Version < 1 || !root.Signed.ConsistentSnapshot {
		return errors.New("trusted root does not enable the required consistent-snapshot policy")
	}
	for _, name := range []string{metadata.ROOT, metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP} {
		role, ok := root.Signed.Roles[name]
		if !ok || role == nil || role.Threshold < 1 || len(role.KeyIDs) < role.Threshold {
			return fmt.Errorf("trusted root role %q is incomplete", name)
		}
		seen := map[string]bool{}
		for _, keyID := range role.KeyIDs {
			if seen[keyID] || root.Signed.Keys[keyID] == nil {
				return fmt.Errorf("trusted root role %q has invalid keys", name)
			}
			seen[keyID] = true
		}
	}
	rootRole := root.Signed.Roles[metadata.ROOT]
	if rootRole.Threshold < 2 || len(rootRole.KeyIDs) < rootRole.Threshold {
		return errors.New("trusted root must retain a multi-key root threshold")
	}
	return nil
}

func validateDelegations(delegations *metadata.Delegations) error {
	if delegations == nil {
		return errors.New("top-level targets must contain release and toolchain delegations")
	}
	roleKeys := map[string]map[string]bool{}
	for _, role := range delegations.Roles {
		if role.Threshold < 1 || len(role.KeyIDs) < role.Threshold {
			return fmt.Errorf("delegated target role %q has an invalid threshold", role.Name)
		}
		keys := map[string]bool{}
		for _, keyID := range role.KeyIDs {
			if keys[keyID] || delegations.Keys[keyID] == nil {
				return fmt.Errorf("delegated target role %q has invalid keys", role.Name)
			}
			keys[keyID] = true
		}

		if role.Name == "releases" || role.Name == "toolchains" {
			if _, exists := roleKeys[role.Name]; exists {
				return fmt.Errorf("duplicate delegated target role %q", role.Name)
			}
			if !role.Terminating || len(role.PathHashPrefixes) != 0 || len(role.Paths) != 1 || role.Paths[0] != role.Name+"/*" {
				return fmt.Errorf("delegated target role %q violates namespace policy", role.Name)
			}
			roleKeys[role.Name] = keys
			continue
		}

		for _, protected := range []string{"releases/_scope_probe", "toolchains/_scope_probe"} {
			matches, err := role.IsDelegatedPath(protected)
			if err != nil {
				return fmt.Errorf("delegated target role %q has invalid path policy: %w", role.Name, err)
			}
			if matches {
				return fmt.Errorf("delegated target role %q overlaps a protected namespace", role.Name)
			}
		}
	}
	releaseKeys, releaseOK := roleKeys["releases"]
	toolchainKeys, toolchainOK := roleKeys["toolchains"]
	if !releaseOK || !toolchainOK {
		return errors.New("release and toolchain delegations are required")
	}
	for keyID := range releaseKeys {
		if toolchainKeys[keyID] {
			return errors.New("release and toolchain delegations must use independent keys")
		}
	}
	return nil
}

func namespacedTargetPath(namespace, relative string) (string, error) {
	if namespace != "releases" && namespace != "toolchains" {
		return "", errors.New("unsupported update target namespace")
	}
	if relative == "" || relative != strings.TrimSpace(relative) || strings.ContainsAny(relative, "\x00\\") ||
		strings.HasPrefix(relative, "/") || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, "../") || path.Clean(relative) != relative {
		return "", errors.New("update target path is invalid")
	}
	return namespace + "/" + relative, nil
}

func validateRemoteMetadataURL(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", errors.New("remote metadata URL is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("remote metadata URL must be an HTTPS repository URL without credentials, query, or fragment")
	}
	return value, nil
}

func validateStateRoot(root string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
		return "", errors.New("release metadata state root must be a clean absolute non-root path")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if resolved != root {
		return "", errors.New("release metadata state root must not traverse symlinks")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0700 != 0700 {
		return "", errors.New("release metadata state root must be a private owner-accessible real directory")
	}
	return root, nil
}

func ensurePrivateDirectory(dir string) error {
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Mode().Perm()&0700 != 0700 {
		return errors.New("release metadata cache directory must be private and real")
	}
	return nil
}

func parseRoot(raw []byte) (*metadata.Metadata[metadata.RootType], error) {
	if len(raw) == 0 || int64(len(raw)) > maxRootMetadataBytes {
		return nil, errors.New("trusted root exceeds size policy")
	}
	root, err := metadata.Root().FromBytes(raw)
	if err != nil {
		return nil, err
	}
	return root, nil
}

func readBoundedRegularFile(filePath string, limit int64) ([]byte, error) {
	fd, err := unix.Open(filePath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(filePath))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return nil, errors.New("release metadata cache file must be a non-writable regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("release metadata cache file exceeds size policy")
	}
	return raw, nil
}

func (c *Client) lock() (*os.File, error) {
	fd, err := unix.Open(c.lockPath, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(c.lockPath))
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		file.Close()
		return nil, errors.New("release metadata lock file must be private and regular")
	}
	if err = unix.Flock(fd, unix.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func unlock(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}
