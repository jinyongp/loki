package lifecycle

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/platform/safeio"
)

const (
	managedIntegrationStateVersion = 1
	maxManagedIntegrationFileBytes = 1 << 20

	ManagedIntegrationStateFile      = "integrations/state.json"
	ManagedGitHubConfigFile          = "integrations/github.toml"
	ManagedSigningPublicInfoFile     = "integrations/signing.json"
	ManagedSigningPublicKeyFile      = "integrations/signing.pub"
	ManagedSigningGitConfigFile      = "integrations/signing.gitconfig"
	ManagedSigningAllowedSignersFile = "integrations/signing-allowed-signers"
	ManagedGitHubCredentialFile      = "credentials/github-app.pem"
	ManagedSigningCredentialFile     = "credentials/signing-key"
)

var managedIntegrationFiles = []string{
	ManagedIntegrationStateFile,
	ManagedGitHubConfigFile,
	ManagedSigningPublicInfoFile,
	ManagedSigningPublicKeyFile,
	ManagedSigningGitConfigFile,
	ManagedSigningAllowedSignersFile,
	ManagedGitHubCredentialFile,
	ManagedSigningCredentialFile,
}

type ManagedIntegrationToggle struct {
	Configured       bool   `json:"configured"`
	Enabled          bool   `json:"enabled"`
	CredentialSHA256 string `json:"credential_sha256,omitempty"`
	ConfigSHA256     string `json:"config_sha256,omitempty"`
}

type ManagedIntegrationState struct {
	Version  int                      `json:"version"`
	Revision string                   `json:"revision,omitempty"`
	Browser  ManagedIntegrationToggle `json:"browser"`
	Signing  ManagedIntegrationToggle `json:"signing"`
	GitHub   ManagedIntegrationToggle `json:"github"`
}

type ManagedSigningPublicInfo struct {
	Version       int    `json:"version"`
	PublicKey     string `json:"public_key"`
	Fingerprint   string `json:"fingerprint"`
	IdentityName  string `json:"identity_name"`
	IdentityEmail string `json:"identity_email"`
}

func (i ManagedSigningPublicInfo) Valid() bool {
	return i.Version == 1 &&
		strings.HasPrefix(i.PublicKey, "ssh-ed25519 ") &&
		len(i.PublicKey) <= 16384 && !strings.ContainsAny(i.PublicKey, "\r\n\x00") &&
		strings.HasPrefix(i.Fingerprint, "SHA256:") &&
		len(i.Fingerprint) <= 256 && !strings.ContainsAny(i.Fingerprint, "\r\n\x00") &&
		strings.TrimSpace(i.IdentityName) == i.IdentityName && i.IdentityName != "" &&
		len(i.IdentityName) <= 256 && !strings.ContainsAny(i.IdentityName, "\r\n\x00") &&
		strings.TrimSpace(i.IdentityEmail) == i.IdentityEmail && i.IdentityEmail != "" &&
		len(i.IdentityEmail) <= 320 && !strings.ContainsAny(i.IdentityEmail, " \t\r\n\x00")
}

func DefaultManagedIntegrationState() ManagedIntegrationState {
	return ManagedIntegrationState{
		Version: managedIntegrationStateVersion,
		Browser: ManagedIntegrationToggle{Configured: true},
	}
}

func (s ManagedIntegrationState) Valid() bool {
	if s.Version != managedIntegrationStateVersion || len(s.Revision) > 128 || strings.ContainsAny(s.Revision, "\r\n\x00") {
		return false
	}
	if !s.Browser.Configured || s.Browser.CredentialSHA256 != "" || s.Browser.ConfigSHA256 != "" {
		return false
	}
	if s.Browser.Enabled && !s.Browser.Configured {
		return false
	}
	for _, item := range []ManagedIntegrationToggle{s.Signing, s.GitHub} {
		if item.Enabled && !item.Configured {
			return false
		}
		if item.Configured {
			if !validManagedIntegrationDigest(item.CredentialSHA256) {
				return false
			}
		} else if item.CredentialSHA256 != "" || item.ConfigSHA256 != "" {
			return false
		}
		if item.ConfigSHA256 != "" && !validManagedIntegrationDigest(item.ConfigSHA256) {
			return false
		}
	}
	return true
}

func validManagedIntegrationDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func ManagedIntegrationFiles() []string {
	return append([]string(nil), managedIntegrationFiles...)
}

func managedIntegrationFilePath(root, relative string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) {
		return "", errors.New("host lifecycle state root is invalid")
	}
	relative = filepath.Clean(strings.TrimSpace(relative))
	allowed := false
	for _, candidate := range managedIntegrationFiles {
		if relative == candidate {
			allowed = true
			break
		}
	}
	if !allowed || filepath.IsAbs(relative) || relative == "." || strings.ContainsRune(relative, 0) {
		return "", errors.New("managed integration file is invalid")
	}
	path := filepath.Join(root, relative)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("managed integration file escapes lifecycle root")
	}
	return path, nil
}

func readManagedPrivateFile(path string, required bool) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("managed integration file must be a private regular file")
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("managed integration file owner is invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxManagedIntegrationFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxManagedIntegrationFileBytes {
		return nil, errors.New("managed integration file exceeds size limit")
	}
	return raw, nil
}

func publishManagedPrivateFile(path string, raw []byte) error {
	if len(raw) == 0 || len(raw) > maxManagedIntegrationFileBytes {
		return errors.New("managed integration file content is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("managed integration directory must be private and real")
	}
	var stat unix.Stat_t
	if err = unix.Stat(parent, &stat); err != nil || int(stat.Uid) != os.Geteuid() {
		return errors.New("managed integration directory owner is invalid")
	}
	if err = safeio.PublishPrivate(path, raw, true); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func ManagedIntegrationDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *FileStore) ReadManagedIntegrations(ctx context.Context) (ManagedIntegrationState, error) {
	if s == nil {
		return ManagedIntegrationState{}, errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return ManagedIntegrationState{}, err
	}
	path, err := managedIntegrationFilePath(s.Root, ManagedIntegrationStateFile)
	if err != nil {
		return ManagedIntegrationState{}, err
	}
	raw, err := readManagedPrivateFile(path, false)
	if err != nil {
		return ManagedIntegrationState{}, err
	}
	if len(raw) == 0 {
		return DefaultManagedIntegrationState(), nil
	}
	var state ManagedIntegrationState
	if err = decodeManagedJSON(raw, &state); err != nil || !state.Valid() {
		return ManagedIntegrationState{}, errors.New("managed integration state is invalid")
	}
	return state, nil
}

func (s *FileStore) CommitManagedIntegrations(ctx context.Context, state ManagedIntegrationState, reason string, now time.Time) error {
	if s == nil {
		return errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 128 || strings.ContainsAny(reason, "\r\n\x00") {
		return errors.New("managed integration revision reason is invalid")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Installed == nil || snapshot.Installation == nil {
		return errors.New("managed integration change requires an installed release")
	}
	state.Version = managedIntegrationStateVersion
	state.Revision = lifecycleRevision(state.Revision, snapshot.Host.Revision, reason, now.UTC())
	if !state.Valid() {
		return errors.New("managed integration state is invalid")
	}
	raw, err := encodeManagedJSON(state)
	if err != nil {
		return err
	}
	path, err := managedIntegrationFilePath(s.Root, ManagedIntegrationStateFile)
	if err != nil {
		return err
	}
	if err = publishManagedPrivateFile(path, raw); err != nil {
		return err
	}
	host := snapshot.Host
	host.Revision = lifecycleRevision(snapshot.Host.Revision, snapshot.Installed.ID, "integration", reason, state.Revision, now.UTC())
	return writePrivateJSON(s.path("host.json"), host)
}

func (s *FileStore) ReadManagedIntegrationFile(ctx context.Context, relative string, required bool) ([]byte, error) {
	if s == nil {
		return nil, errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := managedIntegrationFilePath(s.Root, relative)
	if err != nil {
		return nil, err
	}
	return readManagedPrivateFile(path, required)
}

func (s *FileStore) ReadManagedSigningPublicInfo(ctx context.Context) (ManagedSigningPublicInfo, error) {
	raw, err := s.ReadManagedIntegrationFile(ctx, ManagedSigningPublicInfoFile, false)
	if err != nil {
		return ManagedSigningPublicInfo{}, err
	}
	if len(raw) == 0 {
		return ManagedSigningPublicInfo{}, nil
	}
	var info ManagedSigningPublicInfo
	if err = decodeManagedJSON(raw, &info); err != nil || !info.Valid() {
		return ManagedSigningPublicInfo{}, errors.New("managed signing public info is invalid")
	}
	return info, nil
}

func (s *FileStore) ManagedIntegrationFilePath(relative string) (string, error) {
	if s == nil {
		return "", errors.New("host lifecycle store is not configured")
	}
	return managedIntegrationFilePath(s.Root, relative)
}

func (s *FileStore) WriteManagedIntegrationFile(ctx context.Context, relative string, raw []byte) (string, error) {
	if s == nil {
		return "", errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path, err := managedIntegrationFilePath(s.Root, relative)
	if err != nil {
		return "", err
	}
	if err = publishManagedPrivateFile(path, raw); err != nil {
		return "", err
	}
	return ManagedIntegrationDigest(raw), nil
}

func (s *FileStore) RemoveManagedIntegrationFile(ctx context.Context, relative string) error {
	if s == nil {
		return errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := managedIntegrationFilePath(s.Root, relative)
	if err != nil {
		return err
	}
	return removeIfPresent(path)
}

func encodeManagedJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	raw = append(raw, '\n')
	if len(raw) > maxManagedIntegrationFileBytes {
		return nil, errors.New("managed integration state exceeds size limit")
	}
	return raw, nil
}

func decodeManagedJSON(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("managed integration state contains trailing data")
	}
	return nil
}

const managedIntegrationSnapshotPrefix = "managed:"

type managedIntegrationSnapshotFile struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
}

type managedIntegrationSnapshotManifest struct {
	Version int                                       `json:"version"`
	Files   map[string]managedIntegrationSnapshotFile `json:"files"`
}

func validManagedIntegrationSnapshotRef(ref string) bool {
	if !strings.HasPrefix(ref, managedIntegrationSnapshotPrefix) {
		return false
	}
	raw := strings.TrimPrefix(ref, managedIntegrationSnapshotPrefix)
	return len(raw) == 64 && validManagedIntegrationDigest(raw)
}

func (s *FileStore) managedIntegrationSnapshotRoot() string {
	return s.path("integration-snapshots")
}

func ensureManagedPrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("managed integration snapshot directory must be private and real")
	}
	var stat unix.Stat_t
	if err = unix.Stat(path, &stat); err != nil || int(stat.Uid) != os.Geteuid() {
		return errors.New("managed integration snapshot directory owner is invalid")
	}
	return nil
}

func (s *FileStore) managedIntegrationSnapshotPath(ref string) (string, error) {
	if s == nil || !validManagedIntegrationSnapshotRef(ref) {
		return "", errors.New("managed integration snapshot reference is invalid")
	}
	return filepath.Join(s.managedIntegrationSnapshotRoot(), strings.TrimPrefix(ref, managedIntegrationSnapshotPrefix)), nil
}

func newManagedIntegrationSnapshotRef() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return managedIntegrationSnapshotPrefix + hex.EncodeToString(raw), nil
}

func (s *FileStore) CaptureManagedIntegrationSnapshot(ctx context.Context) (ref string, err error) {
	if s == nil {
		return "", errors.New("host lifecycle store is not configured")
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	root := s.managedIntegrationSnapshotRoot()
	if err = ensureManagedPrivateDirectory(root); err != nil {
		return "", err
	}
	for attempts := 0; attempts < 4; attempts++ {
		ref, err = newManagedIntegrationSnapshotRef()
		if err != nil {
			return "", err
		}
		dir, pathErr := s.managedIntegrationSnapshotPath(ref)
		if pathErr != nil {
			return "", pathErr
		}
		if err = os.Mkdir(dir, 0700); errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		ok := false
		defer func() {
			if !ok {
				_ = os.RemoveAll(dir)
			}
		}()

		manifest := managedIntegrationSnapshotManifest{
			Version: managedIntegrationStateVersion,
			Files:   make(map[string]managedIntegrationSnapshotFile, len(managedIntegrationFiles)),
		}
		for _, relative := range managedIntegrationFiles {
			if err = ctx.Err(); err != nil {
				return "", err
			}
			current, readErr := s.ReadManagedIntegrationFile(ctx, relative, false)
			if readErr != nil {
				return "", readErr
			}
			if len(current) == 0 {
				manifest.Files[relative] = managedIntegrationSnapshotFile{}
				continue
			}
			target := filepath.Join(dir, "files", relative)
			if err = ensureManagedPrivateDirectory(filepath.Dir(target)); err != nil {
				return "", err
			}
			if err = publishManagedPrivateFile(target, current); err != nil {
				return "", err
			}
			manifest.Files[relative] = managedIntegrationSnapshotFile{
				Present: true,
				SHA256:  ManagedIntegrationDigest(current),
			}
		}
		encoded, encodeErr := encodeManagedJSON(manifest)
		if encodeErr != nil {
			return "", encodeErr
		}
		if err = publishManagedPrivateFile(filepath.Join(dir, "manifest.json"), encoded); err != nil {
			return "", err
		}
		directory, openErr := os.Open(dir)
		if openErr != nil {
			return "", openErr
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil {
			return "", syncErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		ok = true
		return ref, nil
	}
	return "", errors.New("cannot allocate managed integration snapshot")
}

func (s *FileStore) managedIntegrationSnapshotDirectory(ref string) (string, error) {
	dir, err := s.managedIntegrationSnapshotPath(ref)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("managed integration snapshot directory is invalid")
	}
	var stat unix.Stat_t
	if err = unix.Stat(dir, &stat); err != nil || int(stat.Uid) != os.Geteuid() {
		return "", errors.New("managed integration snapshot directory owner is invalid")
	}
	return dir, nil
}

func (s *FileStore) loadManagedIntegrationSnapshot(ref string) (managedIntegrationSnapshotManifest, string, error) {
	dir, err := s.managedIntegrationSnapshotDirectory(ref)
	if err != nil {
		return managedIntegrationSnapshotManifest{}, "", err
	}
	raw, err := readManagedPrivateFile(filepath.Join(dir, "manifest.json"), true)
	if err != nil {
		return managedIntegrationSnapshotManifest{}, "", err
	}
	var manifest managedIntegrationSnapshotManifest
	if err = decodeManagedJSON(raw, &manifest); err != nil || manifest.Version != managedIntegrationStateVersion {
		return managedIntegrationSnapshotManifest{}, "", errors.New("managed integration snapshot manifest is invalid")
	}
	if len(manifest.Files) != len(managedIntegrationFiles) {
		return managedIntegrationSnapshotManifest{}, "", errors.New("managed integration snapshot manifest is incomplete")
	}
	for _, relative := range managedIntegrationFiles {
		entry, ok := manifest.Files[relative]
		if !ok {
			return managedIntegrationSnapshotManifest{}, "", errors.New("managed integration snapshot manifest is incomplete")
		}
		if entry.Present {
			if !validManagedIntegrationDigest(entry.SHA256) {
				return managedIntegrationSnapshotManifest{}, "", errors.New("managed integration snapshot file digest is invalid")
			}
		} else if entry.SHA256 != "" {
			return managedIntegrationSnapshotManifest{}, "", errors.New("managed integration snapshot absent file has a digest")
		}
	}
	for relative := range manifest.Files {
		allowed := false
		for _, candidate := range managedIntegrationFiles {
			if relative == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return managedIntegrationSnapshotManifest{}, "", errors.New("managed integration snapshot manifest contains an unknown file")
		}
	}
	return manifest, dir, nil
}

func (s *FileStore) RestoreManagedIntegrationSnapshot(ctx context.Context, ref string) error {
	if s == nil {
		return errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ref == "" {
		for _, relative := range managedIntegrationFiles {
			if err := s.RemoveManagedIntegrationFile(ctx, relative); err != nil {
				return err
			}
		}
		return nil
	}
	manifest, dir, err := s.loadManagedIntegrationSnapshot(ref)
	if err != nil {
		return err
	}
	for _, relative := range managedIntegrationFiles {
		if err = ctx.Err(); err != nil {
			return err
		}
		entry := manifest.Files[relative]
		if !entry.Present {
			if err = s.RemoveManagedIntegrationFile(ctx, relative); err != nil {
				return err
			}
			continue
		}
		raw, readErr := readManagedPrivateFile(filepath.Join(dir, "files", relative), true)
		if readErr != nil {
			return readErr
		}
		if ManagedIntegrationDigest(raw) != entry.SHA256 {
			return errors.New("managed integration snapshot integrity check failed")
		}
		if _, err = s.WriteManagedIntegrationFile(ctx, relative, raw); err != nil {
			return err
		}
	}
	return nil
}

func (s *FileStore) DeleteManagedIntegrationSnapshot(ctx context.Context, ref string) error {
	if s == nil {
		return errors.New("host lifecycle store is not configured")
	}
	if ref == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.managedIntegrationSnapshotPath(ref)
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(dir)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("managed integration snapshot directory is invalid")
	}
	if err = os.RemoveAll(dir); err != nil {
		return err
	}
	root, err := os.Open(s.managedIntegrationSnapshotRoot())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer root.Close()
	return root.Sync()
}

func (s *FileStore) ManagedIntegrationSnapshotUsage(ctx context.Context, ref string) (int64, error) {
	if ref == "" {
		return 0, nil
	}
	dir, err := s.managedIntegrationSnapshotDirectory(ref)
	if err != nil {
		return 0, err
	}
	var total int64
	err = filepath.WalkDir(dir, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("managed integration snapshot contains a symlink")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			if info.Mode().Perm()&0077 != 0 {
				return errors.New("managed integration snapshot contains a public directory")
			}
		case info.Mode().IsRegular():
			if info.Mode().Perm() != 0600 {
				return errors.New("managed integration snapshot contains a non-private file")
			}
			total += info.Size()
		default:
			return errors.New("managed integration snapshot contains an unsupported object")
		}
		return nil
	})
	return total, err
}

func (s *FileStore) ListManagedIntegrationSnapshots(ctx context.Context) ([]RuntimeSnapshotInfo, error) {
	if s == nil {
		return nil, errors.New("host lifecycle store is not configured")
	}
	root := s.managedIntegrationSnapshotRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]RuntimeSnapshotInfo, 0, len(entries))
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		ref := managedIntegrationSnapshotPrefix + entry.Name()
		if !entry.IsDir() || !validManagedIntegrationSnapshotRef(ref) {
			return nil, errors.New("managed integration snapshot root contains an unexpected entry")
		}
		bytes, usageErr := s.ManagedIntegrationSnapshotUsage(ctx, ref)
		if usageErr != nil {
			return nil, usageErr
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil, statErr
		}
		result = append(result, RuntimeSnapshotInfo{Ref: ref, Bytes: bytes, CreatedAt: info.ModTime().UTC()})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Ref < result[j].Ref
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}
