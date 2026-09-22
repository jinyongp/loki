package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"loki/internal/host/releases"
)

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
