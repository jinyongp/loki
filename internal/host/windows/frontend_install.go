package windows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	FrontendOwnershipSchemaVersion = 1
	FrontendArchitecture           = "windows-amd64"
)

var frontendRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type FrontendPaths struct {
	Root            string
	BinDir          string
	Binary          string
	Ownership       string
	HelpersRoot     string
	ConnectionsRoot string
}

func ResolveFrontendPaths(localAppData string) (FrontendPaths, error) {
	root, ok := normalizeWindowsPath(localAppData)
	if !ok || strings.HasPrefix(root, "//") {
		return FrontendPaths{}, errors.New("Windows LOCALAPPDATA must be a local absolute drive path")
	}
	root = strings.ReplaceAll(root, "/", "\\")
	programRoot := joinWindowsPath(root, "Programs\\Loki")
	bin := joinWindowsPath(programRoot, "bin")
	return FrontendPaths{
		Root:            programRoot,
		BinDir:          bin,
		Binary:          joinWindowsPath(bin, "loki.exe"),
		Ownership:       joinWindowsPath(programRoot, "ownership.json"),
		HelpersRoot:     joinWindowsPath(programRoot, "helpers"),
		ConnectionsRoot: joinWindowsPath(programRoot, "connections"),
	}, nil
}

type FrontendOwnership struct {
	SchemaVersion  int    `json:"schema_version"`
	ReleaseTag     string `json:"release_tag"`
	SourceRevision string `json:"source_revision"`
	CanonicalPath  string `json:"canonical_path"`
	SHA256         string `json:"sha256"`
	Length         int64  `json:"length"`
	Architecture   string `json:"architecture"`
	PathEntry      string `json:"path_entry"`
}

func BuildFrontendOwnership(binding ReleaseBinding, paths FrontendPaths, digest string, length int64) ([]byte, error) {
	if !validFrontendReleaseTag(binding.ReleaseTag) || !bindingDigestPattern.MatchString(digest) || length <= 0 ||
		!frontendRevisionPattern.MatchString(binding.SourceRevision) {
		return nil, errors.New("Windows frontend ownership inputs are invalid")
	}
	ownership := FrontendOwnership{
		SchemaVersion:  FrontendOwnershipSchemaVersion,
		ReleaseTag:     binding.ReleaseTag,
		SourceRevision: binding.SourceRevision,
		CanonicalPath:  paths.Binary,
		SHA256:         digest,
		Length:         length,
		Architecture:   FrontendArchitecture,
		PathEntry:      paths.BinDir,
	}
	return json.MarshalIndent(ownership, "", "  ")
}

func ParseFrontendOwnership(raw []byte, paths FrontendPaths) (FrontendOwnership, error) {
	var ownership FrontendOwnership
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ownership); err != nil {
		return FrontendOwnership{}, fmt.Errorf("decode Windows frontend ownership: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return FrontendOwnership{}, err
	}
	if ownership.SchemaVersion != FrontendOwnershipSchemaVersion ||
		!validFrontendReleaseTag(ownership.ReleaseTag) ||
		!frontendRevisionPattern.MatchString(ownership.SourceRevision) ||
		!WindowsPathEqual(ownership.CanonicalPath, paths.Binary) ||
		!bindingDigestPattern.MatchString(ownership.SHA256) ||
		ownership.Length <= 0 ||
		ownership.Architecture != FrontendArchitecture ||
		!WindowsPathEqual(ownership.PathEntry, paths.BinDir) {
		return FrontendOwnership{}, errors.New("Windows frontend ownership does not match the canonical Loki frontend")
	}
	return ownership, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	}
	return errors.New("Windows frontend ownership contains trailing JSON data")
}

func validFrontendReleaseTag(value string) bool {
	return windowsReleaseTagPattern.MatchString(value) && semver.IsValid(value) && semver.Canonical(value) == value
}

type FrontendInstallPlatform interface {
	Lstat(string) (StatePath, error)
	ReadFile(string) ([]byte, error)
	FileDigest(string) (string, int64, error)
	EnsurePrivateDirectory(context.Context, string) error
	PublishExecutable(context.Context, string, string) error
	WriteProtectedAtomic(context.Context, string, []byte) error
	UserPath(context.Context) (string, error)
	SetUserPath(context.Context, string) error
	Sleep(context.Context, time.Duration) error
}

type FrontendInstallDisposition string

const (
	FrontendInstalled FrontendInstallDisposition = "installed"
	FrontendUpgraded  FrontendInstallDisposition = "upgraded"
	FrontendRepaired  FrontendInstallDisposition = "repaired"
)

type FrontendInstallResult struct {
	Disposition FrontendInstallDisposition
	Paths       FrontendPaths
	Ownership   FrontendOwnership
	PathChanged bool
}

type FrontendInstaller struct {
	Platform   FrontendInstallPlatform
	Attempts   int
	RetryDelay time.Duration
}

func (installer FrontendInstaller) Install(
	ctx context.Context,
	sourceExecutable string,
	paths FrontendPaths,
	binding ReleaseBinding,
) (FrontendInstallResult, error) {
	if installer.Platform == nil {
		return FrontendInstallResult{}, errors.New("Windows frontend install platform is unavailable")
	}
	if !validFrontendReleaseTag(binding.ReleaseTag) {
		return FrontendInstallResult{}, errors.New("Windows frontend release tag is invalid")
	}
	sourceDigest, sourceLength, err := installer.Platform.FileDigest(sourceExecutable)
	if err != nil {
		return FrontendInstallResult{}, fmt.Errorf("hash trusted Windows frontend: %w", err)
	}
	if !bindingDigestPattern.MatchString(sourceDigest) || sourceLength <= 0 {
		return FrontendInstallResult{}, errors.New("trusted Windows frontend has invalid file identity")
	}
	targetInfo, err := installer.Platform.Lstat(paths.Binary)
	if err != nil {
		return FrontendInstallResult{}, fmt.Errorf("inspect canonical Windows frontend: %w", err)
	}
	if targetInfo.Exists && (!targetInfo.Regular || targetInfo.Reparse) {
		return FrontendInstallResult{}, errors.New("canonical Windows frontend is not a regular non-reparse file")
	}
	ownershipInfo, err := installer.Platform.Lstat(paths.Ownership)
	if err != nil {
		return FrontendInstallResult{}, fmt.Errorf("inspect Windows frontend ownership: %w", err)
	}
	if ownershipInfo.Exists && (!ownershipInfo.Regular || ownershipInfo.Reparse) {
		return FrontendInstallResult{}, errors.New("Windows frontend ownership is not a regular non-reparse file")
	}

	var targetDigest string
	var targetLength int64
	if targetInfo.Exists {
		targetDigest, targetLength, err = installer.Platform.FileDigest(paths.Binary)
		if err != nil {
			return FrontendInstallResult{}, fmt.Errorf("hash canonical Windows frontend: %w", err)
		}
	}

	var existing FrontendOwnership
	haveOwnership := ownershipInfo.Exists
	if haveOwnership {
		raw, readErr := installer.Platform.ReadFile(paths.Ownership)
		if readErr != nil {
			return FrontendInstallResult{}, fmt.Errorf("read Windows frontend ownership: %w", readErr)
		}
		existing, readErr = ParseFrontendOwnership(raw, paths)
		if readErr != nil {
			return FrontendInstallResult{}, readErr
		}
		if !targetInfo.Exists {
			return FrontendInstallResult{}, errors.New("verified Windows frontend ownership exists but canonical executable is missing")
		}
	}

	disposition := FrontendInstalled
	publish := !targetInfo.Exists
	switch {
	case haveOwnership:
		comparison := semver.Compare(binding.ReleaseTag, existing.ReleaseTag)
		if comparison < 0 {
			return FrontendInstallResult{}, fmt.Errorf("refusing automatic Windows frontend downgrade from %s to %s", existing.ReleaseTag, binding.ReleaseTag)
		}
		targetMatchesExisting := targetDigest == existing.SHA256 && targetLength == existing.Length
		targetMatchesSource := targetDigest == sourceDigest && targetLength == sourceLength
		switch {
		case targetMatchesExisting && comparison == 0:
			if existing.SHA256 != sourceDigest || existing.Length != sourceLength || existing.SourceRevision != binding.SourceRevision {
				return FrontendInstallResult{}, errors.New("same-release Windows frontend bytes do not match verified ownership")
			}
			publish = false
			disposition = FrontendRepaired
		case targetMatchesExisting && comparison > 0:
			publish = true
			disposition = FrontendUpgraded
		case comparison > 0 && targetMatchesSource:
			// The binary replacement completed but ownership publication did not.
			// Recover only when the canonical bytes exactly match this trusted bootstrap.
			publish = false
			disposition = FrontendRepaired
		default:
			return FrontendInstallResult{}, errors.New("canonical Windows frontend no longer matches verified ownership or the accepted upgrade bytes")
		}
	case targetInfo.Exists:
		if targetDigest != sourceDigest || targetLength != sourceLength {
			return FrontendInstallResult{}, errors.New("refusing to replace an unverified canonical Windows frontend")
		}
		publish = false
		disposition = FrontendRepaired
	}

	if err = installer.Platform.EnsurePrivateDirectory(ctx, paths.Root); err != nil {
		return FrontendInstallResult{}, fmt.Errorf("prepare private Windows frontend root: %w", err)
	}
	if err = installer.Platform.EnsurePrivateDirectory(ctx, paths.BinDir); err != nil {
		return FrontendInstallResult{}, fmt.Errorf("prepare private Windows frontend bin directory: %w", err)
	}

	if publish {
		attempts := installer.Attempts
		if attempts <= 0 {
			attempts = 5
		}
		delay := installer.RetryDelay
		if delay <= 0 {
			delay = 250 * time.Millisecond
		}
		var publishErr error
		for attempt := 0; attempt < attempts; attempt++ {
			publishErr = installer.Platform.PublishExecutable(ctx, sourceExecutable, paths.Binary)
			if publishErr == nil {
				break
			}
			if attempt+1 == attempts {
				return FrontendInstallResult{}, fmt.Errorf("replace canonical Windows frontend after %d attempts: %w", attempts, publishErr)
			}
			if sleepErr := installer.Platform.Sleep(ctx, delay); sleepErr != nil {
				return FrontendInstallResult{}, sleepErr
			}
		}
		digest, length, digestErr := installer.Platform.FileDigest(paths.Binary)
		if digestErr != nil {
			return FrontendInstallResult{}, fmt.Errorf("verify published Windows frontend: %w", digestErr)
		}
		if digest != sourceDigest || length != sourceLength {
			return FrontendInstallResult{}, errors.New("published Windows frontend does not match trusted source bytes")
		}
	}

	raw, err := BuildFrontendOwnership(binding, paths, sourceDigest, sourceLength)
	if err != nil {
		return FrontendInstallResult{}, err
	}
	if err = installer.Platform.WriteProtectedAtomic(ctx, paths.Ownership, raw); err != nil {
		return FrontendInstallResult{}, fmt.Errorf("publish Windows frontend ownership: %w", err)
	}
	ownership, err := ParseFrontendOwnership(raw, paths)
	if err != nil {
		return FrontendInstallResult{}, err
	}

	currentPath, err := installer.Platform.UserPath(ctx)
	if err != nil {
		return FrontendInstallResult{}, fmt.Errorf("read current-user PATH: %w", err)
	}
	nextPath, changed, err := ReconcileUserPath(currentPath, paths.BinDir)
	if err != nil {
		return FrontendInstallResult{}, err
	}
	if changed {
		if err = installer.Platform.SetUserPath(ctx, nextPath); err != nil {
			return FrontendInstallResult{}, fmt.Errorf("persist current-user PATH; canonical Loki CLI remains at %s: %w", paths.Binary, err)
		}
	}
	return FrontendInstallResult{
		Disposition: disposition,
		Paths:       paths,
		Ownership:   ownership,
		PathChanged: changed,
	}, nil
}

func ReconcileUserPath(current, managed string) (string, bool, error) {
	if _, ok := normalizeWindowsPath(managed); !ok {
		return "", false, errors.New("managed Windows PATH entry is invalid")
	}
	parts := strings.Split(current, ";")
	out := make([]string, 0, len(parts)+1)
	found := false
	for _, part := range parts {
		if part != "" && WindowsPathEqual(part, managed) {
			if !found {
				out = append(out, managed)
				found = true
			}
			continue
		}
		out = append(out, part)
	}
	next := strings.Join(out, ";")
	if !found {
		switch {
		case next == "":
			next = managed
		case strings.HasSuffix(next, ";"):
			next += managed
		default:
			next += ";" + managed
		}
	}
	return next, next != current, nil
}
