package windows

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	PublicApplianceInstallerURL = "https://jinyongp.dev/loki/install.sh"
	maxApplianceInstallerBytes  = int64(64 << 10)
)

type ApplianceReleasePointer struct {
	ReleaseTag string
}

func (pointer ApplianceReleasePointer) Valid() bool {
	return validFrontendReleaseTag(pointer.ReleaseTag)
}

type ApplianceReleaseFetcher interface {
	Fetch(context.Context, string, int64) ([]byte, error)
}

type HTTPApplianceReleaseFetcher struct {
	Client *http.Client
}

func (fetcher HTTPApplianceReleaseFetcher) Fetch(ctx context.Context, assetURL string, maximum int64) ([]byte, error) {
	if maximum <= 0 || maximum > maxApplianceInstallerBytes {
		return nil, errors.New("appliance release pointer download bound is invalid")
	}
	client := fetcher.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("appliance release pointer returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, errors.New("appliance release pointer exceeds download bound")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("appliance release pointer exceeds download bound")
	}
	return raw, ctx.Err()
}

type ApplianceReleaseClient struct {
	Fetcher      ApplianceReleaseFetcher
	InstallerURL string
}

func (client ApplianceReleaseClient) fetcher() ApplianceReleaseFetcher {
	if client.Fetcher != nil {
		return client.Fetcher
	}
	return HTTPApplianceReleaseFetcher{}
}

func (client ApplianceReleaseClient) installerURL() string {
	if strings.TrimSpace(client.InstallerURL) != "" {
		return client.InstallerURL
	}
	return PublicApplianceInstallerURL
}

func (client ApplianceReleaseClient) Resolve(ctx context.Context) (ApplianceReleasePointer, error) {
	raw, err := client.fetcher().Fetch(ctx, client.installerURL(), maxApplianceInstallerBytes)
	if err != nil {
		return ApplianceReleasePointer{}, fmt.Errorf("fetch public appliance installer: %w", err)
	}
	return ParseApplianceReleasePointer(raw)
}

func (client ApplianceReleaseClient) Compare(installedRelease string, pointer ApplianceReleasePointer) (int, error) {
	installedTag := "v" + strings.TrimPrefix(strings.TrimSpace(installedRelease), "v")
	if !validFrontendReleaseTag(installedTag) || !pointer.Valid() {
		return 0, errors.New("appliance release comparison identity is invalid")
	}
	return semver.Compare(installedTag, pointer.ReleaseTag), nil
}

func ParseApplianceReleasePointer(raw []byte) (ApplianceReleasePointer, error) {
	const prefix = "release_tag='"
	var value string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "'") {
			continue
		}
		if value != "" {
			return ApplianceReleasePointer{}, errors.New("public appliance installer contains duplicate release identity")
		}
		value = strings.TrimSuffix(strings.TrimPrefix(line, prefix), "'")
	}
	pointer := ApplianceReleasePointer{ReleaseTag: strings.TrimSpace(value)}
	if !pointer.Valid() || strings.ContainsAny(pointer.ReleaseTag, "\r\n\x00'") {
		return ApplianceReleasePointer{}, errors.New("public appliance installer release identity is missing or invalid")
	}
	return pointer, nil
}
