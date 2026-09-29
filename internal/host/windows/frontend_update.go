package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/semver"
	"loki/internal/progress"
)

const (
	PublicWindowsInstallerURL = "https://jinyongp.dev/loki/install.ps1"
	windowsFrontendAssetName  = "loki-windows-amd64.exe"
	maxWindowsInstallerBytes  = int64(64 << 10)
	maxWindowsFrontendBytes   = int64(256 << 20)
)

type FrontendReleasePointer struct {
	ReleaseTag string
	SHA256     string
	Length     int64
}

func (pointer FrontendReleasePointer) Valid() bool {
	return validFrontendReleaseTag(pointer.ReleaseTag) &&
		bindingDigestPattern.MatchString(pointer.SHA256) &&
		pointer.Length > 0 && pointer.Length <= maxWindowsFrontendBytes
}

type FrontendUpdateFetcher interface {
	Fetch(context.Context, string, int64) ([]byte, error)
}

type HTTPFrontendUpdateFetcher struct {
	Client   *http.Client
	Progress progress.Reporter
}

func (fetcher HTTPFrontendUpdateFetcher) Fetch(ctx context.Context, assetURL string, maximum int64) ([]byte, error) {
	if maximum <= 0 || maximum > maxWindowsFrontendBytes {
		return nil, errors.New("Windows frontend update download bound is invalid")
	}
	client := fetcher.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
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
		return nil, fmt.Errorf("Windows frontend update returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, errors.New("Windows frontend update response exceeds download bound")
	}
	reader := io.Reader(io.LimitReader(response.Body, maximum+1))
	if maximum >= 1<<20 {
		total := response.ContentLength
		if total < 0 {
			total = 0
		}
		reader = progress.NewReader(reader, fetcher.Progress, progress.ReaderOptions{
			Operation: "update", Phase: "download-frontend", Label: "Windows frontend", TotalBytes: total,
		})
	}
	stopHeartbeat := func() {}
	if maximum >= 1<<20 {
		stopHeartbeat = progress.StartHeartbeat(ctx, fetcher.Progress, progress.HeartbeatOptions{
			Operation: "update", Phase: "download-frontend", Message: "Still downloading the Windows frontend",
		})
	}
	raw, err := io.ReadAll(reader)
	stopHeartbeat()
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("Windows frontend update response exceeds download bound")
	}
	return raw, nil
}

type FrontendReleaseClient struct {
	Fetcher      FrontendUpdateFetcher
	InstallerURL string
	Progress     progress.Reporter
}

func (client FrontendReleaseClient) fetcher() FrontendUpdateFetcher {
	if client.Fetcher != nil {
		return client.Fetcher
	}
	return HTTPFrontendUpdateFetcher{Progress: client.Progress}
}

func (client FrontendReleaseClient) installerURL() string {
	if strings.TrimSpace(client.InstallerURL) != "" {
		return client.InstallerURL
	}
	return PublicWindowsInstallerURL
}

func (client FrontendReleaseClient) Resolve(ctx context.Context) (FrontendReleasePointer, error) {
	raw, err := client.fetcher().Fetch(ctx, client.installerURL(), maxWindowsInstallerBytes)
	if err != nil {
		return FrontendReleasePointer{}, fmt.Errorf("fetch public Windows installer: %w", err)
	}
	pointer, err := ParseFrontendReleasePointer(raw)
	if err != nil {
		return FrontendReleasePointer{}, err
	}
	return pointer, nil
}

func (client FrontendReleaseClient) Download(ctx context.Context, pointer FrontendReleasePointer) ([]byte, error) {
	if !pointer.Valid() {
		return nil, errors.New("Windows frontend release pointer is invalid")
	}
	assetURL, err := windowsFrontendReleaseAssetURL(pointer.ReleaseTag)
	if err != nil {
		return nil, err
	}
	raw, err := client.fetcher().Fetch(ctx, assetURL, pointer.Length)
	if err != nil {
		return nil, fmt.Errorf("download Windows frontend %s: %w", pointer.ReleaseTag, err)
	}
	if int64(len(raw)) != pointer.Length {
		return nil, errors.New("downloaded Windows frontend length does not match release pointer")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != pointer.SHA256 {
		return nil, errors.New("downloaded Windows frontend SHA-256 does not match release pointer")
	}
	return raw, nil
}

func (client FrontendReleaseClient) Compare(currentTag string, pointer FrontendReleasePointer) (int, error) {
	if !validFrontendReleaseTag(currentTag) || !pointer.Valid() {
		return 0, errors.New("Windows frontend release comparison identity is invalid")
	}
	return semver.Compare(currentTag, pointer.ReleaseTag), nil
}

func ParseFrontendReleasePointer(raw []byte) (FrontendReleasePointer, error) {
	tag, err := singlePowerShellString(raw, "$releaseTag")
	if err != nil {
		return FrontendReleasePointer{}, err
	}
	digest, err := singlePowerShellString(raw, "$frontendSha256")
	if err != nil {
		return FrontendReleasePointer{}, err
	}
	lengthText, err := singlePowerShellInt64String(raw, "$frontendLength")
	if err != nil {
		return FrontendReleasePointer{}, err
	}
	length, err := strconv.ParseInt(lengthText, 10, 64)
	if err != nil {
		return FrontendReleasePointer{}, errors.New("public Windows installer frontend length is invalid")
	}
	pointer := FrontendReleasePointer{
		ReleaseTag: strings.TrimSpace(tag),
		SHA256:     strings.TrimSpace(digest),
		Length:     length,
	}
	if !pointer.Valid() {
		return FrontendReleasePointer{}, errors.New("public Windows installer release pointer is invalid")
	}
	return pointer, nil
}

func singlePowerShellString(raw []byte, variable string) (string, error) {
	return singlePowerShellAssignment(raw, variable+" = \"", "\"")
}

func singlePowerShellInt64String(raw []byte, variable string) (string, error) {
	return singlePowerShellAssignment(raw, variable+" = [Int64]\"", "\"")
}

func singlePowerShellAssignment(raw []byte, prefix, suffix string) (string, error) {
	var value string
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
			continue
		}
		if value != "" {
			return "", errors.New("public Windows installer contains duplicate release identity")
		}
		value = strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
	}
	if value == "" || strings.ContainsAny(value, "\r\n\x00\"") {
		return "", errors.New("public Windows installer release identity is missing or invalid")
	}
	return value, nil
}

func windowsFrontendReleaseAssetURL(tag string) (string, error) {
	if !validFrontendReleaseTag(tag) {
		return "", errors.New("Windows frontend release tag is invalid")
	}
	return (&url.URL{
		Scheme: "https",
		Host:   "github.com",
		Path:   "/jinyongp/loki/releases/download/" + tag + "/" + windowsFrontendAssetName,
	}).String(), nil
}
