package windows

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"loki/internal/host/connect"
)

const (
	HelperOwnershipSchemaVersion = 1
	helperCatalogAssetName       = "loki-connect-helpers.json"
	maxRuntimeCatalogBytes       = int64(4 << 20)
	maxRuntimeHelperBytes        = int64(1 << 30)
	helperOwnershipFileName      = "ownership.json"
)

type HelperDownloader interface {
	Fetch(context.Context, string, int64) ([]byte, error)
}

type HelperInstallPlatform interface {
	Lstat(string) (StatePath, error)
	ReadFile(string) ([]byte, error)
	FileDigest(string) (string, int64, error)
	ListDirectory(string) ([]string, error)
	EnsurePrivateDirectory(context.Context, string) error
	VerifyPrivatePath(string, bool) error
	CreatePrivateTempDirectory(context.Context, string, string) (string, error)
	WritePrivateFile(context.Context, string, []byte) error
	PublishDirectory(string, string) error
	RemoveTree(string) error
}

type HelperFileIdentity struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Length int64  `json:"length"`
}

type HelperOwnership struct {
	SchemaVersion int                  `json:"schema_version"`
	HelperID      string               `json:"helper_id"`
	Provider      string               `json:"provider"`
	Version       string               `json:"version"`
	Platform      string               `json:"platform"`
	Executable    string               `json:"executable"`
	ArchiveSHA256 string               `json:"archive_sha256"`
	ArchiveLength int64                `json:"archive_length"`
	Files         []HelperFileIdentity `json:"files"`
}

type ManagedHelper struct {
	Helper         connect.Helper
	Root           string
	ExecutablePath string
	Reused         bool
}

type HelperManager struct {
	Platform    HelperInstallPlatform
	Downloader  HelperDownloader
	Binding     ReleaseBinding
	HelpersRoot string
}

func (manager HelperManager) Ensure(ctx context.Context, helperID, platformName string) (ManagedHelper, error) {
	if manager.Platform == nil || manager.Downloader == nil {
		return ManagedHelper{}, errors.New("Windows helper manager adapters are incomplete")
	}
	if !validFrontendReleaseTag(manager.Binding.ReleaseTag) ||
		!bindingDigestPattern.MatchString(manager.Binding.HelperCatalog.SHA256) ||
		manager.Binding.HelperCatalog.Length <= 0 {
		return ManagedHelper{}, errors.New("Windows helper manager release binding is invalid")
	}
	if strings.TrimSpace(manager.HelpersRoot) == "" {
		return ManagedHelper{}, errors.New("Windows helper root is empty")
	}
	catalog, err := manager.loadCatalog(ctx)
	if err != nil {
		return ManagedHelper{}, err
	}
	helper, err := selectHelper(catalog, helperID, platformName)
	if err != nil {
		return ManagedHelper{}, err
	}
	target := joinWindowsPath(manager.HelpersRoot, helper.ID+"\\"+helper.Version+"\\"+helper.Platform)
	if existing, err := manager.inspectInstalled(target, helper); err != nil {
		return ManagedHelper{}, err
	} else if existing != nil {
		return *existing, nil
	}

	archiveURL, err := helperMirrorURL(manager.Binding.ReleaseTag, helper.Archive.MirrorAsset)
	if err != nil {
		return ManagedHelper{}, err
	}
	archiveRaw, err := manager.Downloader.Fetch(ctx, archiveURL, minPositive(helper.Archive.SourceLength+1, maxRuntimeHelperBytes+1))
	if err != nil {
		return ManagedHelper{}, fmt.Errorf("download mirrored helper archive: %w", err)
	}
	if err = verifyBoundBytes(archiveRaw, helper.Archive.SourceSHA256, helper.Archive.SourceLength, maxRuntimeHelperBytes); err != nil {
		return ManagedHelper{}, fmt.Errorf("verify mirrored helper archive: %w", err)
	}
	files, err := extractHelperArchive(archiveRaw, helper)
	if err != nil {
		return ManagedHelper{}, err
	}

	parent := joinWindowsPath(manager.HelpersRoot, helper.ID+"\\"+helper.Version)
	for _, directory := range []string{manager.HelpersRoot, joinWindowsPath(manager.HelpersRoot, helper.ID), parent} {
		if err = manager.Platform.EnsurePrivateDirectory(ctx, directory); err != nil {
			return ManagedHelper{}, fmt.Errorf("prepare private helper directory %s: %w", directory, err)
		}
		if err = manager.Platform.VerifyPrivatePath(directory, true); err != nil {
			return ManagedHelper{}, fmt.Errorf("verify private helper directory %s: %w", directory, err)
		}
	}
	staging, err := manager.Platform.CreatePrivateTempDirectory(ctx, parent, ".loki-helper-")
	if err != nil {
		return ManagedHelper{}, fmt.Errorf("create helper staging directory: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = manager.Platform.RemoveTree(staging)
		}
	}()
	if err = manager.Platform.VerifyPrivatePath(staging, true); err != nil {
		return ManagedHelper{}, fmt.Errorf("verify helper staging ACL: %w", err)
	}

	identities := make([]HelperFileIdentity, 0, len(files))
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		raw := files[name]
		filePath := joinWindowsPath(staging, name)
		if err = manager.Platform.WritePrivateFile(ctx, filePath, raw); err != nil {
			return ManagedHelper{}, fmt.Errorf("write helper archive member %s: %w", name, err)
		}
		if err = manager.Platform.VerifyPrivatePath(filePath, false); err != nil {
			return ManagedHelper{}, fmt.Errorf("verify helper archive member ACL %s: %w", name, err)
		}
		sum := sha256.Sum256(raw)
		identities = append(identities, HelperFileIdentity{
			Name: name, SHA256: hex.EncodeToString(sum[:]), Length: int64(len(raw)),
		})
	}
	ownership := HelperOwnership{
		SchemaVersion: HelperOwnershipSchemaVersion,
		HelperID:      helper.ID, Provider: helper.Provider, Version: helper.Version,
		Platform: helper.Platform, Executable: helper.Executable,
		ArchiveSHA256: helper.Archive.SourceSHA256, ArchiveLength: helper.Archive.SourceLength,
		Files: identities,
	}
	ownershipRaw, err := encodeHelperOwnership(ownership)
	if err != nil {
		return ManagedHelper{}, err
	}
	if err = manager.Platform.WritePrivateFile(ctx, joinWindowsPath(staging, helperOwnershipFileName), ownershipRaw); err != nil {
		return ManagedHelper{}, fmt.Errorf("write helper ownership: %w", err)
	}
	if err = manager.Platform.VerifyPrivatePath(joinWindowsPath(staging, helperOwnershipFileName), false); err != nil {
		return ManagedHelper{}, fmt.Errorf("verify helper ownership ACL: %w", err)
	}

	if state, statErr := manager.Platform.Lstat(target); statErr != nil {
		return ManagedHelper{}, statErr
	} else if state.Exists {
		existing, inspectErr := manager.inspectInstalled(target, helper)
		if inspectErr != nil {
			return ManagedHelper{}, inspectErr
		}
		if existing == nil {
			return ManagedHelper{}, errors.New("helper target appeared concurrently without valid Loki ownership")
		}
		return *existing, nil
	}
	if err = manager.Platform.PublishDirectory(staging, target); err != nil {
		if existing, inspectErr := manager.inspectInstalled(target, helper); inspectErr == nil && existing != nil {
			return *existing, nil
		}
		return ManagedHelper{}, fmt.Errorf("publish managed helper atomically: %w", err)
	}
	cleanup = false
	installed, err := manager.inspectInstalled(target, helper)
	if err != nil {
		return ManagedHelper{}, err
	}
	if installed == nil {
		return ManagedHelper{}, errors.New("published helper is not visible after atomic install")
	}
	installed.Reused = false
	return *installed, nil
}

func (manager HelperManager) loadCatalog(ctx context.Context) (connect.Catalog, error) {
	url, err := helperMirrorURL(manager.Binding.ReleaseTag, helperCatalogAssetName)
	if err != nil {
		return connect.Catalog{}, err
	}
	raw, err := manager.Downloader.Fetch(ctx, url, minPositive(manager.Binding.HelperCatalog.Length+1, maxRuntimeCatalogBytes+1))
	if err != nil {
		return connect.Catalog{}, fmt.Errorf("download release-bound helper catalog: %w", err)
	}
	if err = verifyBoundBytes(raw, manager.Binding.HelperCatalog.SHA256, manager.Binding.HelperCatalog.Length, maxRuntimeCatalogBytes); err != nil {
		return connect.Catalog{}, fmt.Errorf("verify release-bound helper catalog: %w", err)
	}
	catalog, err := connect.LoadCatalog(raw)
	if err != nil {
		return connect.Catalog{}, err
	}
	return catalog, nil
}

func (manager HelperManager) inspectInstalled(target string, helper connect.Helper) (*ManagedHelper, error) {
	state, err := manager.Platform.Lstat(target)
	if err != nil {
		return nil, err
	}
	if !state.Exists {
		return nil, nil
	}
	if !state.Directory || state.Reparse {
		return nil, errors.New("managed helper target is not a real directory")
	}
	if err = manager.Platform.VerifyPrivatePath(target, true); err != nil {
		return nil, fmt.Errorf("managed helper directory ACL drift: %w", err)
	}
	raw, err := manager.Platform.ReadFile(joinWindowsPath(target, helperOwnershipFileName))
	if err != nil {
		return nil, fmt.Errorf("read managed helper ownership: %w", err)
	}
	ownership, err := parseHelperOwnership(raw)
	if err != nil {
		return nil, err
	}
	if err = validateHelperOwnership(ownership, helper); err != nil {
		return nil, err
	}
	actualNames, err := manager.Platform.ListDirectory(target)
	if err != nil {
		return nil, err
	}
	expectedNames := append([]string(nil), helper.ArchiveMembers...)
	expectedNames = append(expectedNames, helperOwnershipFileName)
	slices.Sort(expectedNames)
	slices.Sort(actualNames)
	if !slices.Equal(actualNames, expectedNames) {
		return nil, errors.New("managed helper directory contains unexpected or missing files")
	}
	for _, identity := range ownership.Files {
		filePath := joinWindowsPath(target, identity.Name)
		info, statErr := manager.Platform.Lstat(filePath)
		if statErr != nil {
			return nil, statErr
		}
		if !info.Exists || !info.Regular || info.Reparse {
			return nil, fmt.Errorf("managed helper file %s is not a regular non-reparse file", identity.Name)
		}
		if err = manager.Platform.VerifyPrivatePath(filePath, false); err != nil {
			return nil, fmt.Errorf("managed helper file ACL drift for %s: %w", identity.Name, err)
		}
		digest, length, digestErr := manager.Platform.FileDigest(filePath)
		if digestErr != nil {
			return nil, digestErr
		}
		if digest != identity.SHA256 || length != identity.Length {
			return nil, fmt.Errorf("managed helper file %s no longer matches installed ownership", identity.Name)
		}
	}
	executable := joinWindowsPath(target, helper.Executable)
	return &ManagedHelper{Helper: helper, Root: target, ExecutablePath: executable, Reused: true}, nil
}

func selectHelper(catalog connect.Catalog, helperID, platformName string) (connect.Helper, error) {
	helperID = strings.TrimSpace(helperID)
	platformName = strings.TrimSpace(platformName)
	for _, helper := range catalog.Helpers {
		if helper.ID == helperID && helper.Platform == platformName {
			return helper, nil
		}
	}
	return connect.Helper{}, fmt.Errorf("release does not contain managed helper %q for %q", helperID, platformName)
}

func helperMirrorURL(releaseTag, asset string) (string, error) {
	if !validFrontendReleaseTag(releaseTag) {
		return "", errors.New("helper mirror release tag is invalid")
	}
	if asset == "" || asset != path.Base(asset) || strings.ContainsAny(asset, "/\\\x00") {
		return "", errors.New("helper mirror asset name is invalid")
	}
	return "https://github.com/jinyongp/loki/releases/download/" + releaseTag + "/" + asset, nil
}

func verifyBoundBytes(raw []byte, expectedDigest string, expectedLength, maxLength int64) error {
	if expectedLength <= 0 || expectedLength > maxLength || int64(len(raw)) != expectedLength {
		return errors.New("downloaded file length does not match release identity")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != expectedDigest {
		return errors.New("downloaded file SHA-256 does not match release identity")
	}
	return nil
}

func extractHelperArchive(raw []byte, helper connect.Helper) (map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("open mirrored helper archive: %w", err)
	}
	expected := make(map[string]bool, len(helper.ArchiveMembers))
	for _, name := range helper.ArchiveMembers {
		expected[name] = true
	}
	out := make(map[string][]byte, len(expected))
	var total int64
	for _, entry := range reader.File {
		name := entry.Name
		if !expected[name] || name != path.Base(name) || strings.ContainsAny(name, "\\\x00") {
			return nil, fmt.Errorf("helper archive contains unexpected member %q", name)
		}
		if _, duplicate := out[name]; duplicate {
			return nil, fmt.Errorf("helper archive contains duplicate member %q", name)
		}
		mode := entry.Mode()
		if !mode.IsRegular() || mode&os.ModeSymlink != 0 || entry.Flags&0x1 != 0 {
			return nil, fmt.Errorf("helper archive member %q is not a plain regular file", name)
		}
		if entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > uint64(maxRuntimeHelperBytes) {
			return nil, fmt.Errorf("helper archive member %q has invalid size", name)
		}
		total += int64(entry.UncompressedSize64)
		if total > maxRuntimeHelperBytes {
			return nil, errors.New("helper archive expanded size exceeds safety bound")
		}
		file, openErr := entry.Open()
		if openErr != nil {
			return nil, fmt.Errorf("open helper archive member %q: %w", name, openErr)
		}
		member, readErr := io.ReadAll(io.LimitReader(file, int64(entry.UncompressedSize64)+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read helper archive member %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if uint64(len(member)) != entry.UncompressedSize64 {
			return nil, fmt.Errorf("helper archive member %q changed size while reading", name)
		}
		out[name] = member
	}
	if len(out) != len(expected) {
		return nil, errors.New("helper archive is missing expected members")
	}
	return out, nil
}

func encodeHelperOwnership(ownership HelperOwnership) ([]byte, error) {
	if err := validateHelperOwnershipShape(ownership); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(ownership, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func parseHelperOwnership(raw []byte) (HelperOwnership, error) {
	var ownership HelperOwnership
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ownership); err != nil {
		return HelperOwnership{}, fmt.Errorf("decode managed helper ownership: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return HelperOwnership{}, err
	}
	if err := validateHelperOwnershipShape(ownership); err != nil {
		return HelperOwnership{}, err
	}
	return ownership, nil
}

func validateHelperOwnershipShape(ownership HelperOwnership) error {
	if ownership.SchemaVersion != HelperOwnershipSchemaVersion ||
		ownership.HelperID == "" || ownership.Provider == "" || ownership.Version == "" ||
		ownership.Platform == "" || ownership.Executable == "" ||
		!bindingDigestPattern.MatchString(ownership.ArchiveSHA256) || ownership.ArchiveLength <= 0 ||
		len(ownership.Files) == 0 {
		return errors.New("managed helper ownership is invalid")
	}
	seen := map[string]bool{}
	for _, file := range ownership.Files {
		if file.Name == "" || file.Name != path.Base(file.Name) || seen[file.Name] ||
			!bindingDigestPattern.MatchString(file.SHA256) || file.Length <= 0 {
			return errors.New("managed helper file ownership is invalid")
		}
		seen[file.Name] = true
	}
	return nil
}

func validateHelperOwnership(ownership HelperOwnership, helper connect.Helper) error {
	if err := validateHelperOwnershipShape(ownership); err != nil {
		return err
	}
	if ownership.HelperID != helper.ID || ownership.Provider != helper.Provider ||
		ownership.Version != helper.Version || ownership.Platform != helper.Platform ||
		ownership.Executable != helper.Executable ||
		ownership.ArchiveSHA256 != helper.Archive.SourceSHA256 ||
		ownership.ArchiveLength != helper.Archive.SourceLength {
		return errors.New("managed helper ownership does not match release catalog")
	}
	names := make([]string, 0, len(ownership.Files))
	for _, file := range ownership.Files {
		names = append(names, file.Name)
	}
	slices.Sort(names)
	expected := append([]string(nil), helper.ArchiveMembers...)
	slices.Sort(expected)
	if !slices.Equal(names, expected) {
		return errors.New("managed helper ownership file set does not match release catalog")
	}
	return nil
}

func minPositive(left, right int64) int64 {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}
