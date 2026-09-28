package windows

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type staticFrontendUpdateFetcher struct {
	responses map[string][]byte
	err       error
}

func (fetcher staticFrontendUpdateFetcher) Fetch(_ context.Context, url string, maximum int64) ([]byte, error) {
	if fetcher.err != nil {
		return nil, fetcher.err
	}
	raw, ok := fetcher.responses[url]
	if !ok {
		return nil, errors.New("unexpected URL")
	}
	if int64(len(raw)) > maximum {
		return nil, errors.New("response exceeds maximum")
	}
	return append([]byte(nil), raw...), nil
}

func TestParseFrontendReleasePointer(t *testing.T) {
	raw := []byte(strings.Join([]string{
		`$releaseTag = "v0.1.23"`,
		`$frontendSha256 = "` + strings.Repeat("a", 64) + `"`,
		`$frontendLength = [Int64]"8339456"`,
	}, "\r\n"))
	pointer, err := ParseFrontendReleasePointer(raw)
	if err != nil {
		t.Fatal(err)
	}
	if pointer.ReleaseTag != "v0.1.23" || pointer.SHA256 != strings.Repeat("a", 64) || pointer.Length != 8339456 {
		t.Fatalf("pointer=%+v", pointer)
	}
}

func TestParseFrontendReleasePointerRejectsDuplicatesAndInvalidIdentity(t *testing.T) {
	for name, raw := range map[string]string{
		"duplicate": `$releaseTag = "v0.1.23"
$releaseTag = "v0.1.24"
$frontendSha256 = "` + strings.Repeat("a", 64) + `"
$frontendLength = [Int64]"10"`,
		"bad-tag": `$releaseTag = "latest"
$frontendSha256 = "` + strings.Repeat("a", 64) + `"
$frontendLength = [Int64]"10"`,
		"bad-digest": `$releaseTag = "v0.1.23"
$frontendSha256 = "ABC"
$frontendLength = [Int64]"10"`,
		"bad-length": `$releaseTag = "v0.1.23"
$frontendSha256 = "` + strings.Repeat("a", 64) + `"
$frontendLength = [Int64]"0"`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFrontendReleasePointer([]byte(raw)); err == nil {
				t.Fatalf("invalid pointer accepted: %s", raw)
			}
		})
	}
}

func TestFrontendReleaseClientDownloadVerifiesExactBytes(t *testing.T) {
	raw := []byte("verified windows frontend")
	sum := sha256.Sum256(raw)
	pointer := FrontendReleasePointer{
		ReleaseTag: "v0.1.24",
		SHA256:     hex.EncodeToString(sum[:]),
		Length:     int64(len(raw)),
	}
	assetURL, err := windowsFrontendReleaseAssetURL(pointer.ReleaseTag)
	if err != nil {
		t.Fatal(err)
	}
	client := FrontendReleaseClient{Fetcher: staticFrontendUpdateFetcher{
		responses: map[string][]byte{assetURL: raw},
	}}
	got, err := client.Download(t.Context(), pointer)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Fatalf("download=%q", got)
	}

	bad := pointer
	bad.SHA256 = strings.Repeat("b", 64)
	if _, err = client.Download(t.Context(), bad); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}

func TestFrontendReleaseClientCompare(t *testing.T) {
	pointer := FrontendReleasePointer{
		ReleaseTag: "v0.1.24",
		SHA256:     strings.Repeat("a", 64),
		Length:     10,
	}
	client := FrontendReleaseClient{}
	if comparison, err := client.Compare("v0.1.23", pointer); err != nil || comparison >= 0 {
		t.Fatalf("comparison=%d err=%v", comparison, err)
	}
	if comparison, err := client.Compare("v0.1.24", pointer); err != nil || comparison != 0 {
		t.Fatalf("comparison=%d err=%v", comparison, err)
	}
	if comparison, err := client.Compare("v0.1.25", pointer); err != nil || comparison <= 0 {
		t.Fatalf("comparison=%d err=%v", comparison, err)
	}
}

func TestHTTPFrontendUpdateFetcherCancellationAndBounds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (HTTPFrontendUpdateFetcher{Client: server.Client()}).Fetch(ctx, server.URL, 1024); err == nil {
		t.Fatal("cancelled frontend update request succeeded")
	}
	for _, limit := range []int64{0, maxWindowsFrontendBytes + 1} {
		if _, err := (HTTPFrontendUpdateFetcher{}).Fetch(t.Context(), "https://example.invalid", limit); err == nil {
			t.Fatalf("invalid bound %d accepted", limit)
		}
	}
}
