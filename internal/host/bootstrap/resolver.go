package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"loki/internal/host/releases"
)

const maxHostIdentityBytes = 64 << 10

type ReleaseSource interface {
	FetchRelease(context.Context, string) (releases.TargetDescriptor, []byte, error)
}

type Candidate struct {
	Entry         releases.ReleaseIndexEntry
	Manifest      releases.ReleaseManifest
	ManifestBytes []byte
	Host          releases.SupportedHost
	Binary        []byte
}

func Resolve(ctx context.Context, source ReleaseSource, host releases.SupportedHost, requestedRelease string) (Candidate, error) {
	if source == nil {
		return Candidate{}, errors.New("bootstrap release source is not configured")
	}
	indexDescriptor, indexRaw, err := source.FetchRelease(ctx, "index.json")
	if err != nil {
		return Candidate{}, fmt.Errorf("fetch authenticated release index: %w", err)
	}
	if indexDescriptor.Path != "releases/index.json" {
		return Candidate{}, errors.New("authenticated release index target path is invalid")
	}
	if err = indexDescriptor.VerifyBytes(indexRaw); err != nil {
		return Candidate{}, fmt.Errorf("verify authenticated release index target: %w", err)
	}
	index, err := releases.LoadReleaseIndex(indexRaw)
	if err != nil {
		return Candidate{}, err
	}
	entry, err := selectRelease(index, strings.TrimSpace(requestedRelease))
	if err != nil {
		return Candidate{}, err
	}

	manifestRelative := strings.TrimPrefix(entry.Manifest.Path, "releases/")
	manifestDescriptor, manifestRaw, err := source.FetchRelease(ctx, manifestRelative)
	if err != nil {
		return Candidate{}, fmt.Errorf("fetch authenticated release manifest: %w", err)
	}
	if manifestDescriptor != entry.Manifest {
		return Candidate{}, errors.New("authenticated release manifest descriptor does not match the release index")
	}
	manifest, err := entry.VerifyManifest(manifestRaw)
	if err != nil {
		return Candidate{}, err
	}
	if !supportsHost(manifest.SupportedHosts, host) {
		return Candidate{}, fmt.Errorf(
			"release %s does not support %s/%s %s %s",
			entry.Release, host.Environment, host.Distribution, host.Version, host.Arch,
		)
	}

	binaryRelative := strings.TrimPrefix(manifest.HostBinary.Path, "releases/")
	binaryDescriptor, binaryRaw, err := source.FetchRelease(ctx, binaryRelative)
	if err != nil {
		return Candidate{}, fmt.Errorf("fetch authenticated host binary: %w", err)
	}
	if binaryDescriptor != manifest.HostBinary {
		return Candidate{}, errors.New("authenticated host binary descriptor does not match the release manifest")
	}
	if err = manifest.HostBinary.VerifyBytes(binaryRaw); err != nil {
		return Candidate{}, err
	}
	return Candidate{
		Entry:         entry,
		Manifest:      manifest,
		ManifestBytes: append([]byte(nil), manifestRaw...),
		Host:          host,
		Binary:        append([]byte(nil), binaryRaw...),
	}, nil
}

func selectRelease(index releases.ReleaseIndex, requested string) (releases.ReleaseIndexEntry, error) {
	if err := index.Validate(); err != nil {
		return releases.ReleaseIndexEntry{}, err
	}
	if requested == "" {
		return index.Entries[len(index.Entries)-1], nil
	}
	for _, entry := range index.Entries {
		if entry.Release == requested {
			return entry, nil
		}
	}
	return releases.ReleaseIndexEntry{}, fmt.Errorf("release %q is not present in the authenticated index", requested)
}

func supportsHost(supported []releases.SupportedHost, host releases.SupportedHost) bool {
	for _, candidate := range supported {
		if candidate == host {
			return true
		}
	}
	return false
}

func DetectHost() (releases.SupportedHost, error) {
	osRelease, err := readBoundedFile("/etc/os-release", maxHostIdentityBytes)
	if err != nil {
		return releases.SupportedHost{}, err
	}
	kernelRelease, err := readBoundedFile("/proc/sys/kernel/osrelease", maxHostIdentityBytes)
	if err != nil {
		return releases.SupportedHost{}, err
	}
	return detectHost(runtime.GOOS, runtime.GOARCH, osRelease, kernelRelease)
}

func detectHost(goos, goarch string, osRelease, kernelRelease []byte) (releases.SupportedHost, error) {
	if goos != "linux" || goarch != "amd64" {
		return releases.SupportedHost{}, fmt.Errorf("unsupported bootstrap platform %s/%s", goos, goarch)
	}
	values, err := parseOSRelease(osRelease)
	if err != nil {
		return releases.SupportedHost{}, err
	}
	distribution := strings.ToLower(values["ID"])
	version := values["VERSION_ID"]
	if distribution != "ubuntu" || version != "24.04" {
		return releases.SupportedHost{}, fmt.Errorf("unsupported bootstrap host %s %s", distribution, version)
	}
	environment := "native"
	if strings.Contains(strings.ToLower(string(kernelRelease)), "microsoft") {
		environment = "wsl"
	}
	return releases.SupportedHost{
		Environment:  environment,
		Distribution: distribution,
		Version:      version,
		Arch:         goarch,
	}, nil
}

func parseOSRelease(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || len(raw) > maxHostIdentityBytes {
		return nil, errors.New("os-release exceeds bootstrap size policy")
	}
	result := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			value = value[1 : len(value)-1]
			value = strings.ReplaceAll(value, `\\"`, `"`)
			value = strings.ReplaceAll(value, `\\`, `\`)
		}
		if key == "ID" || key == "VERSION_ID" {
			if value == "" || strings.ContainsAny(value, "\r\n\x00") {
				return nil, errors.New("os-release contains invalid host identity")
			}
			result[key] = value
		}
	}
	if result["ID"] == "" || result["VERSION_ID"] == "" {
		return nil, errors.New("os-release is missing host identity")
	}
	return result, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("host identity file exceeds bootstrap size policy")
	}
	return bytes.Clone(raw), nil
}
