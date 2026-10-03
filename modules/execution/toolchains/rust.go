package toolchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const rustDefaultHost = "x86_64-unknown-linux-gnu"

var (
	rustNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	rustVersionPattern = regexp.MustCompile(`^([0-9]+)\.([0-9]+)(?:\.([0-9]+))?(?:-(beta)(?:\.([0-9]+))?|-nightly)?$`)
)

type RustChannelSelector struct {
	Channel       string
	Date          string
	VersionPrefix string
}

func ParseRustChannel(raw string) (RustChannelSelector, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n\x00/\\") {
		return RustChannelSelector{}, errors.New("Rust toolchain channel is invalid")
	}
	for _, channel := range []string{"stable", "beta", "nightly"} {
		if raw == channel {
			return RustChannelSelector{Channel: channel}, nil
		}
		prefix := channel + "-"
		if strings.HasPrefix(raw, prefix) {
			date := strings.TrimPrefix(raw, prefix)
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return RustChannelSelector{}, errors.New("Rust dated channel must use YYYY-MM-DD")
			}
			return RustChannelSelector{Channel: channel, Date: date}, nil
		}
	}
	match := rustVersionPattern.FindStringSubmatch(raw)
	if match == nil {
		return RustChannelSelector{}, fmt.Errorf("unsupported Rust toolchain channel %q", raw)
	}
	if len(match[3]) == 0 {
		for _, value := range []string{match[1], match[2]} {
			if len(value) > 1 && value[0] == '0' {
				return RustChannelSelector{}, errors.New("Rust version components cannot contain leading zeroes")
			}
		}
		return RustChannelSelector{VersionPrefix: match[1] + "." + match[2]}, nil
	}
	normalized, err := normalizeRustVersion(raw)
	if err != nil {
		return RustChannelSelector{}, err
	}
	return RustChannelSelector{VersionPrefix: normalized}, nil
}

func normalizeRustVersion(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	match := rustVersionPattern.FindStringSubmatch(raw)
	if match == nil || match[3] == "" {
		return "", fmt.Errorf("Rust release version %q must include major.minor.patch", raw)
	}
	for _, value := range []string{match[1], match[2], match[3]} {
		if len(value) > 1 && value[0] == '0' {
			return "", errors.New("Rust version components cannot contain leading zeroes")
		}
	}
	if match[4] == "beta" && match[5] == "" {
		return "", errors.New("Rust beta release version must include its beta number")
	}
	return raw, nil
}

func NormalizeRustVersion(raw string) (string, error) {
	return normalizeRustVersion(raw)
}

func compareRustVersions(left, right string) (int, error) {
	leftParts, leftPre, leftNumber, err := parseRustComparableVersion(left)
	if err != nil {
		return 0, err
	}
	rightParts, rightPre, rightNumber, err := parseRustComparableVersion(right)
	if err != nil {
		return 0, err
	}
	for index := range leftParts {
		if leftParts[index] < rightParts[index] {
			return -1, nil
		}
		if leftParts[index] > rightParts[index] {
			return 1, nil
		}
	}
	rank := func(pre string) int {
		switch pre {
		case "nightly":
			return 0
		case "beta":
			return 1
		default:
			return 2
		}
	}
	if rank(leftPre) < rank(rightPre) {
		return -1, nil
	}
	if rank(leftPre) > rank(rightPre) {
		return 1, nil
	}
	if leftNumber < rightNumber {
		return -1, nil
	}
	if leftNumber > rightNumber {
		return 1, nil
	}
	return 0, nil
}

func parseRustComparableVersion(raw string) ([3]uint64, string, uint64, error) {
	var result [3]uint64
	normalized, err := normalizeRustVersion(raw)
	if err != nil {
		return result, "", 0, err
	}
	match := rustVersionPattern.FindStringSubmatch(normalized)
	for index := 0; index < 3; index++ {
		parts, parseErr := parseNumericVersionParts(match[index+1], 1, 1)
		if parseErr != nil {
			return result, "", 0, parseErr
		}
		result[index] = parts[0]
	}
	pre := ""
	number := uint64(0)
	if strings.HasSuffix(normalized, "-nightly") {
		pre = "nightly"
	} else if match[4] == "beta" {
		pre = "beta"
		if match[5] != "" {
			parts, parseErr := parseNumericVersionParts(match[5], 1, 1)
			if parseErr != nil {
				return result, "", 0, parseErr
			}
			number = parts[0]
		}
	}
	return result, pre, number, nil
}

type RustArtifact struct {
	Component       string `json:"component"`
	Target          string `json:"target,omitempty"`
	Filename        string `json:"filename"`
	URL             string `json:"url"`
	SHA256          string `json:"sha256"`
	StripComponents int    `json:"strip_components"`
}

func (a RustArtifact) key() string {
	return a.Component + "@" + a.Target
}

func (a RustArtifact) Validate(release RustRelease) error {
	if !rustNamePattern.MatchString(a.Component) {
		return errors.New("Rust artifact component is invalid")
	}
	if a.Target != "" && !rustNamePattern.MatchString(a.Target) {
		return errors.New("Rust artifact target is invalid")
	}
	if filepath.Base(a.Filename) != a.Filename || !strings.HasSuffix(a.Filename, ".tar.xz") {
		return errors.New("Rust artifact filename must be a local tar.xz filename")
	}
	versionToken := release.Version
	if release.Channel == "beta" || release.Channel == "nightly" {
		versionToken = release.Channel
	}
	if !strings.Contains(a.Filename, "-"+versionToken) {
		return errors.New("Rust artifact filename does not bind the release channel/version")
	}
	if a.Target != "" && !strings.Contains(a.Filename, a.Target) {
		return errors.New("Rust artifact filename does not bind its target")
	}
	if !sha256Text.MatchString(a.SHA256) {
		return errors.New("Rust artifact checksum must be a lowercase sha256 digest")
	}
	if a.StripComponents < 0 || a.StripComponents > 3 {
		return errors.New("Rust artifact strip-components is outside the supported range")
	}
	parsed, err := url.Parse(a.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "static.rust-lang.org" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("Rust artifact URL must use the official static.rust-lang.org HTTPS origin")
	}
	expectedPath := "/dist/" + release.Date + "/" + a.Filename
	if parsed.Path != expectedPath {
		return fmt.Errorf("Rust artifact URL path must be %s", expectedPath)
	}
	return nil
}

type RustRelease struct {
	Channel   string         `json:"channel"`
	Version   string         `json:"version"`
	Date      string         `json:"date"`
	Host      string         `json:"host"`
	Artifacts []RustArtifact `json:"artifacts"`
}

func (r RustRelease) Validate() error {
	if r.Channel != "stable" && r.Channel != "beta" && r.Channel != "nightly" {
		return errors.New("Rust release channel is invalid")
	}
	version, err := normalizeRustVersion(r.Version)
	if err != nil || version != r.Version {
		return errors.New("Rust release version must be canonical")
	}
	switch r.Channel {
	case "stable":
		if strings.Contains(r.Version, "-") {
			return errors.New("stable Rust release cannot use a prerelease version")
		}
	case "beta":
		if !strings.Contains(r.Version, "-beta.") {
			return errors.New("beta Rust release must use a beta prerelease version")
		}
	case "nightly":
		if !strings.HasSuffix(r.Version, "-nightly") {
			return errors.New("nightly Rust release must use a nightly prerelease version")
		}
	}
	if _, err = time.Parse("2006-01-02", r.Date); err != nil {
		return errors.New("Rust release date must use YYYY-MM-DD")
	}
	if r.Host != rustDefaultHost {
		return fmt.Errorf("unsupported Rust host %q", r.Host)
	}
	if len(r.Artifacts) == 0 {
		return errors.New("Rust release has no component artifacts")
	}
	previous := ""
	available := make(map[string]bool, len(r.Artifacts))
	for _, artifact := range r.Artifacts {
		if err = artifact.Validate(r); err != nil {
			return err
		}
		if artifact.key() <= previous {
			return errors.New("Rust release artifacts must be unique and sorted")
		}
		previous = artifact.key()
		available[artifact.key()] = true
	}
	for _, required := range []string{"cargo@" + r.Host, "rust-std@" + r.Host, "rustc@" + r.Host} {
		if !available[required] {
			return fmt.Errorf("Rust release is missing required component %s", required)
		}
	}
	return nil
}

type RustProjectRequest struct {
	Channel    string
	Profile    string
	Components []string
	Targets    []string
}

type normalizedRustRequest struct {
	Selector   RustChannelSelector
	Profile    string
	Components []string
	Targets    []string
}

func normalizeRustProjectRequest(request RustProjectRequest) (normalizedRustRequest, error) {
	selector, err := ParseRustChannel(request.Channel)
	if err != nil {
		return normalizedRustRequest{}, err
	}
	profile := strings.TrimSpace(request.Profile)
	if profile == "" {
		profile = "default"
	}
	if profile != "minimal" && profile != "default" && profile != "complete" {
		return normalizedRustRequest{}, errors.New("Rust profile must be minimal, default, or complete")
	}
	components := []string{"cargo", "rustc"}
	if profile == "default" {
		components = append(components, "clippy", "rust-docs", "rustfmt")
	}
	for _, component := range request.Components {
		component = strings.TrimSpace(component)
		if !rustNamePattern.MatchString(component) || component == "rust-std" {
			return normalizedRustRequest{}, fmt.Errorf("unsupported Rust component %q", component)
		}
		components = append(components, component)
	}
	components = uniqueSortedStrings(components)

	targets := []string{rustDefaultHost}
	for _, target := range request.Targets {
		target = strings.TrimSpace(target)
		if !rustNamePattern.MatchString(target) {
			return normalizedRustRequest{}, fmt.Errorf("invalid Rust target %q", target)
		}
		targets = append(targets, target)
	}
	targets = uniqueSortedStrings(targets)
	return normalizedRustRequest{
		Selector: selector, Profile: profile, Components: components, Targets: targets,
	}, nil
}

func uniqueSortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	output := result[:0]
	for _, value := range result {
		if len(output) == 0 || output[len(output)-1] != value {
			output = append(output, value)
		}
	}
	return output
}

type RustResolution struct {
	Channel   string
	Version   string
	Date      string
	Installed bool
	Acquire   bool
}

type RustPlan struct {
	Request      RustProjectRequest
	Resolution   RustResolution
	Release      RustRelease
	Artifacts    []RustArtifact
	GenerationID string
}

type RustProvider struct {
	Store GenerationStore
}

func (r RustRelease) GenerationID() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "loki-rust-generation:v2\\n%s\\n%s\\n%s\\n%s\\n",
		r.Channel, r.Version, r.Date, r.Host)
	for _, artifact := range r.Artifacts {
		fmt.Fprintf(&builder, "artifact:%s:%s:%s:%s:%d\\n",
			artifact.Component, artifact.Target, artifact.URL, artifact.SHA256, artifact.StripComponents)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func (p RustProvider) Resolve(request RustProjectRequest, releases []RustRelease, update bool) (RustPlan, error) {
	normalized, err := normalizeRustProjectRequest(request)
	if err != nil {
		return RustPlan{}, err
	}
	if len(releases) == 0 {
		return RustPlan{}, errors.New("Rust trusted release set is empty")
	}
	type candidate struct {
		release   RustRelease
		artifacts []RustArtifact
		id        string
		installed bool
	}
	var candidates []candidate
	for _, release := range releases {
		if err = release.Validate(); err != nil {
			return RustPlan{}, err
		}
		if !rustReleaseMatches(normalized.Selector, release) {
			continue
		}
		artifacts, selectErr := rustArtifactsForRequest(release, normalized)
		if selectErr != nil {
			continue
		}
		id := release.GenerationID()
		_, lookupErr := p.Store.Lookup(id)
		installed := lookupErr == nil
		if lookupErr != nil && !errors.Is(lookupErr, os.ErrNotExist) {
			return RustPlan{}, lookupErr
		}
		candidates = append(candidates, candidate{
			release: release, artifacts: artifacts, id: id, installed: installed,
		})
	}
	if len(candidates) == 0 {
		return RustPlan{}, errors.New("Rust selector/components/targets have no administrator-permitted release")
	}
	sort.Slice(candidates, func(i, j int) bool {
		return compareRustRelease(candidates[i].release, candidates[j].release) < 0
	})
	selected := candidates[len(candidates)-1]
	if !update {
		for index := len(candidates) - 1; index >= 0; index-- {
			if candidates[index].installed {
				selected = candidates[index]
				break
			}
		}
	}
	return RustPlan{
		Request: RustProjectRequest{
			Channel: request.Channel, Profile: normalized.Profile,
			Components: append([]string(nil), normalized.Components...),
			Targets:    append([]string(nil), normalized.Targets...),
		},
		Resolution: RustResolution{
			Channel: selected.release.Channel, Version: selected.release.Version, Date: selected.release.Date,
			Installed: selected.installed, Acquire: !selected.installed,
		},
		Release: selected.release, Artifacts: append([]RustArtifact(nil), selected.artifacts...),
		GenerationID: selected.id,
	}, nil
}

func rustReleaseMatches(selector RustChannelSelector, release RustRelease) bool {
	if selector.Channel != "" {
		if release.Channel != selector.Channel {
			return false
		}
		return selector.Date == "" || release.Date == selector.Date
	}
	if strings.Count(selector.VersionPrefix, ".") == 1 {
		return release.Channel == "stable" && strings.HasPrefix(release.Version, selector.VersionPrefix+".")
	}
	return release.Version == selector.VersionPrefix
}

func compareRustRelease(left, right RustRelease) int {
	if left.Date < right.Date {
		return -1
	}
	if left.Date > right.Date {
		return 1
	}
	comparison, err := compareRustVersions(left.Version, right.Version)
	if err != nil {
		return strings.Compare(left.Version, right.Version)
	}
	return comparison
}

func rustRequestContents(release RustRelease, request normalizedRustRequest) normalizedRustRequest {
	if request.Profile != "complete" {
		return request
	}
	result := request
	for _, artifact := range release.Artifacts {
		if artifact.Component == "rust-std" {
			continue
		}
		if artifact.Target == "" || artifact.Target == release.Host {
			result.Components = append(result.Components, artifact.Component)
		}
	}
	result.Components = uniqueSortedStrings(result.Components)
	return result
}

func rustArtifactsForRequest(release RustRelease, request normalizedRustRequest) ([]RustArtifact, error) {
	request = rustRequestContents(release, request)
	byKey := make(map[string]RustArtifact, len(release.Artifacts))
	for _, artifact := range release.Artifacts {
		byKey[artifact.key()] = artifact
	}
	var required []string
	for _, component := range request.Components {
		target := release.Host
		if component == "rust-src" {
			target = ""
		}
		required = append(required, component+"@"+target)
	}
	for _, target := range request.Targets {
		required = append(required, "rust-std@"+target)
	}
	required = uniqueSortedStrings(required)
	artifacts := make([]RustArtifact, 0, len(required))
	for _, key := range required {
		artifact, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("Rust release %s does not provide %s", release.Version, key)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, nil
}

func (p RustProvider) Provision(ctx context.Context, plan RustPlan, artifactDirectory string) (Generation, error) {
	normalized, err := normalizeRustProjectRequest(plan.Request)
	if err != nil {
		return Generation{}, err
	}
	if err = plan.Release.Validate(); err != nil {
		return Generation{}, err
	}
	requestedArtifacts, err := rustArtifactsForRequest(plan.Release, normalized)
	if err != nil {
		return Generation{}, err
	}
	if len(requestedArtifacts) != len(plan.Artifacts) {
		return Generation{}, errors.New("Rust install plan artifact selection is inconsistent")
	}
	for index := range requestedArtifacts {
		if requestedArtifacts[index] != plan.Artifacts[index] {
			return Generation{}, errors.New("Rust install plan artifact selection is inconsistent")
		}
	}
	artifacts := append([]RustArtifact(nil), plan.Release.Artifacts...)
	expectedID := plan.Release.GenerationID()
	if plan.GenerationID != expectedID || plan.Resolution.Version != plan.Release.Version ||
		plan.Resolution.Channel != plan.Release.Channel || plan.Resolution.Date != plan.Release.Date {
		return Generation{}, errors.New("Rust install plan identity is inconsistent")
	}
	if !filepath.IsAbs(artifactDirectory) || filepath.Clean(artifactDirectory) != artifactDirectory {
		return Generation{}, errors.New("Rust artifact directory must be a clean absolute path")
	}
	info, err := os.Lstat(artifactDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Generation{}, errors.New("Rust artifact directory must be a real directory")
	}
	unlock, err := p.lockRustArtifacts(ctx, artifacts)
	if err != nil {
		return Generation{}, err
	}
	defer unlock()

	for _, artifact := range artifacts {
		source := filepath.Join(artifactDirectory, artifact.Filename)
		if filepath.Dir(source) != artifactDirectory {
			return Generation{}, errors.New("Rust artifact filename escapes bundle")
		}
		info, statErr := os.Lstat(source)
		if statErr != nil || !info.Mode().IsRegular() {
			return Generation{}, fmt.Errorf("Rust artifact %s is unavailable", artifact.Filename)
		}
		digest, digestErr := fileDigest(source)
		if digestErr != nil {
			return Generation{}, digestErr
		}
		if digest != artifact.SHA256 {
			return Generation{}, fmt.Errorf("Rust artifact %s checksum mismatch", artifact.Filename)
		}
	}
	return p.Store.Provision(ctx, expectedID, func(ctx context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "rust", plan.Release.Version)
		parts := filepath.Join(base, "parts")
		active := filepath.Join(base, "active")
		if err := os.MkdirAll(parts, 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(active, 0755); err != nil {
			return err
		}
		for index, artifact := range artifacts {
			partName := fmt.Sprintf("%02d-%s", index, strings.ReplaceAll(artifact.key(), "@", "-"))
			installPath := "/opt/loki/toolchain/rust/" + plan.Release.Version + "/parts/" + partName
			source := filepath.Join(artifactDirectory, artifact.Filename)
			if err := installArtifact(ctx, root, source, Artifact{
				Name:    "rust-" + strings.ReplaceAll(partName, "_", "-"),
				Version: plan.Release.Version, Filename: artifact.Filename,
				URL: artifact.URL, SHA256: artifact.SHA256, Format: "tar.xz",
				InstallPath: installPath, StripComponents: artifact.StripComponents,
			}); err != nil {
				return err
			}
			if err := mergeRustPart(filepath.Join(parts, partName), active); err != nil {
				return err
			}
		}
		return validateRustGeneration(active, rustReleaseContents(plan.Release))
	})
}

func rustReleaseContents(release RustRelease) normalizedRustRequest {
	components := make([]string, 0, len(release.Artifacts))
	targets := make([]string, 0, len(release.Artifacts))
	for _, artifact := range release.Artifacts {
		if artifact.Component == "rust-std" {
			targets = append(targets, artifact.Target)
			continue
		}
		components = append(components, artifact.Component)
	}
	return normalizedRustRequest{
		Profile: "managed", Components: uniqueSortedStrings(components), Targets: uniqueSortedStrings(targets),
	}
}

func (p RustProvider) lockRustArtifacts(ctx context.Context, artifacts []RustArtifact) (func(), error) {
	digests := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		digests = append(digests, artifact.SHA256)
	}
	digests = uniqueSortedStrings(digests)
	var releases []func()
	for _, digest := range digests {
		release, err := p.Store.LockArtifact(ctx, digest)
		if err != nil {
			for index := len(releases) - 1; index >= 0; index-- {
				releases[index]()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	return func() {
		for index := len(releases) - 1; index >= 0; index-- {
			releases[index]()
		}
	}, nil
}

func mergeRustPart(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." || relative == ".loki-artifact.json" {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.IsDir() {
			if err = os.MkdirAll(target, info.Mode().Perm()); err != nil {
				return err
			}
			return nil
		}
		if existing, statErr := os.Lstat(target); statErr == nil {
			return verifyRustMergeCollision(path, target, info, existing)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("Rust component contains unsupported object %q", relative)
		}
		return os.Link(path, target)
	})
}

func verifyRustMergeCollision(source, target string, sourceInfo, targetInfo os.FileInfo) error {
	if sourceInfo.IsDir() && targetInfo.IsDir() {
		return nil
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 && targetInfo.Mode()&os.ModeSymlink != 0 {
		left, err := os.Readlink(source)
		if err != nil {
			return err
		}
		right, err := os.Readlink(target)
		if err != nil {
			return err
		}
		if left == right {
			return nil
		}
	}
	if sourceInfo.Mode().IsRegular() && targetInfo.Mode().IsRegular() &&
		sourceInfo.Mode().Perm() == targetInfo.Mode().Perm() {
		left, err := fileDigest(source)
		if err != nil {
			return err
		}
		right, err := fileDigest(target)
		if err != nil {
			return err
		}
		if left == right {
			return nil
		}
	}
	return fmt.Errorf("Rust component collision at %s", target)
}

func validateRustGeneration(active string, request normalizedRustRequest) error {
	for _, executable := range []string{"rustc", "cargo"} {
		if err := requireRustExecutable(filepath.Join(active, "bin", executable)); err != nil {
			return err
		}
	}
	optionalExecutables := map[string][]string{
		"rustfmt":       {"rustfmt", "cargo-fmt"},
		"clippy":        {"clippy-driver", "cargo-clippy"},
		"rust-analyzer": {"rust-analyzer"},
	}
	for component, executables := range optionalExecutables {
		if !containsString(request.Components, component) {
			continue
		}
		for _, executable := range executables {
			if err := requireRustExecutable(filepath.Join(active, "bin", executable)); err != nil {
				return err
			}
		}
	}
	for _, target := range request.Targets {
		path := filepath.Join(active, "lib", "rustlib", target, "lib")
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("Rust target %s standard library is unavailable", target)
		}
	}
	if containsString(request.Components, "rust-src") {
		path := filepath.Join(active, "lib", "rustlib", "src", "rust")
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return errors.New("Rust source component is unavailable")
		}
	}
	return nil
}

func requireRustExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return fmt.Errorf("Rust executable %s is unavailable", filepath.Base(path))
	}
	return nil
}

func containsString(values []string, value string) bool {
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}

func (p RustPlan) Executables(generation Generation) (map[string]string, error) {
	if generation.ID != p.GenerationID {
		return nil, errors.New("Rust generation does not match install plan")
	}
	normalized, err := normalizeRustProjectRequest(p.Request)
	if err != nil {
		return nil, err
	}
	normalized = rustRequestContents(p.Release, normalized)
	active := filepath.Join(generation.Root, "opt", "loki", "toolchain", "rust", p.Release.Version, "active")
	if err = validateRustGeneration(active, normalized); err != nil {
		return nil, err
	}
	result := map[string]string{
		"rustc": filepath.Join(active, "bin", "rustc"),
		"cargo": filepath.Join(active, "bin", "cargo"),
	}
	if containsString(normalized.Components, "rustfmt") {
		result["rustfmt"] = filepath.Join(active, "bin", "rustfmt")
		result["cargo-fmt"] = filepath.Join(active, "bin", "cargo-fmt")
	}
	if containsString(normalized.Components, "clippy") {
		result["clippy-driver"] = filepath.Join(active, "bin", "clippy-driver")
		result["cargo-clippy"] = filepath.Join(active, "bin", "cargo-clippy")
	}
	if containsString(normalized.Components, "rust-analyzer") {
		result["rust-analyzer"] = filepath.Join(active, "bin", "rust-analyzer")
	}
	return result, nil
}
