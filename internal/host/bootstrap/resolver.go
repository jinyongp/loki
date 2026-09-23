package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"loki/internal/host/releases"
)

const publicReleaseHost = "github.com"

type AssetFetcher interface {
	Fetch(context.Context, string, int64) ([]byte, error)
}

type HTTPFetcher struct {
	Client *http.Client
}

func (f HTTPFetcher) Fetch(ctx context.Context, assetURL string, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		return nil, errors.New("release asset size limit is invalid")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	client := f.Client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download release asset: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, errors.New("release asset exceeds manifest size")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("release asset exceeds manifest size")
	}
	return raw, nil
}

type Candidate struct {
	Manifest      releases.ReleaseManifest
	ManifestBytes []byte
	Host          releases.SupportedHost
	Binary        []byte
}

func Resolve(
	ctx context.Context,
	manifestRaw []byte,
	host releases.SupportedHost,
	releaseTag string,
	fetcher AssetFetcher,
) (Candidate, error) {
	if fetcher == nil {
		fetcher = HTTPFetcher{}
	}
	manifest, err := releases.LoadReleaseManifest(manifestRaw)
	if err != nil {
		return Candidate{}, fmt.Errorf("load embedded release manifest: %w", err)
	}
	expectedTag := "v" + manifest.Generation.Spec.Version
	if strings.TrimSpace(releaseTag) != expectedTag {
		return Candidate{}, fmt.Errorf("bootstrap release tag must be %s", expectedTag)
	}
	if !supportsHost(manifest.SupportedHosts, host) {
		return Candidate{}, fmt.Errorf(
			"release %s does not support %s/%s %s %s",
			manifest.Generation.Spec.Version, host.Environment, host.Distribution, host.Version, host.Arch,
		)
	}
	assetURL, err := releaseAssetURL(expectedTag, "loki-linux-amd64")
	if err != nil {
		return Candidate{}, err
	}
	binaryRaw, err := fetcher.Fetch(ctx, assetURL, manifest.HostBinary.Length)
	if err != nil {
		return Candidate{}, fmt.Errorf("fetch release host binary: %w", err)
	}
	if err = manifest.HostBinary.VerifyBytes(binaryRaw); err != nil {
		return Candidate{}, fmt.Errorf("verify release host binary: %w", err)
	}
	return Candidate{
		Manifest:      manifest,
		ManifestBytes: append([]byte(nil), manifestRaw...),
		Host:          host,
		Binary:        append([]byte(nil), binaryRaw...),
	}, nil
}

func releaseAssetURL(tag, asset string) (string, error) {
	if tag == "" || asset == "" || strings.ContainsAny(tag+asset, "/\\\x00") {
		return "", errors.New("release asset identity is invalid")
	}
	result := &url.URL{
		Scheme: "https",
		Host:   publicReleaseHost,
		Path:   "/jinyongp/loki/releases/download/" + tag + "/" + asset,
	}
	return result.String(), nil
}

func supportsHost(supported []releases.SupportedHost, host releases.SupportedHost) bool {
	for _, candidate := range supported {
		if candidate == host {
			return true
		}
	}
	return false
}
