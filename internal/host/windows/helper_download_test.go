package windows

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// This transport is a test-only closed network: only reviewed Loki mirror
// requests can reach the local TLS server. There is no external-network path,
// including the reviewed upstream SourceURL recorded in the helper catalog.
type closedMirrorTransport struct {
	base    http.RoundTripper
	mirror  *url.URL
	allowed map[string]bool
	mu      sync.Mutex
	calls   []string
}

func (transport *closedMirrorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.calls = append(transport.calls, request.URL.String())
	transport.mu.Unlock()
	if !transport.allowed[request.URL.String()] || request.Method != http.MethodGet {
		return nil, errors.New("network policy blocks non-Loki-mirror request")
	}
	local := request.Clone(request.Context())
	localURL := *request.URL
	localURL.Scheme, localURL.Host = transport.mirror.Scheme, transport.mirror.Host
	local.URL = &localURL
	local.Host = transport.mirror.Host
	return transport.base.RoundTrip(local)
}

func TestHelperManagerHTTPMirrorWorksWithoutUpstreamNetwork(t *testing.T) {
	manager, platform, fixture, helper := helperManagerFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		original := "https://github.com" + request.URL.RequestURI()
		raw, ok := fixture.files[original]
		if !ok {
			http.NotFound(w, request)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	mirror, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	catalogURL, _ := helperMirrorURL(manager.Binding.ReleaseTag, helperCatalogAssetName)
	archiveURL, _ := helperMirrorURL(manager.Binding.ReleaseTag, helper.Archive.MirrorAsset)
	transport := &closedMirrorTransport{
		base: server.Client().Transport, mirror: mirror,
		allowed: map[string]bool{catalogURL: true, archiveURL: true},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	manager.Downloader = HTTPHelperDownloader{Client: client}
	first, err := manager.Ensure(t.Context(), helper.ID, helper.Platform)
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused {
		t.Fatal("fresh HTTP fixture unexpectedly reused an installed helper")
	}
	second, err := manager.Ensure(t.Context(), helper.ID, helper.Platform)
	if err != nil || !second.Reused {
		t.Fatalf("verified HTTP mirror reuse failed: %v", err)
	}
	if !reflect.DeepEqual(transport.calls, []string{catalogURL, archiveURL, catalogURL}) {
		t.Fatalf("runtime requested unexpected endpoints: %v", transport.calls)
	}
	// Prove the network policy really denies the catalog's upstream endpoint,
	// rather than relying on upstream access simply not being needed this time.
	if _, err = manager.Downloader.Fetch(t.Context(), helper.Archive.SourceURL, 1024); err == nil {
		t.Fatal("closed-network fixture allowed upstream access")
	}
	// The real HTTP path still cannot override the existing file identity.
	platform.files[first.ExecutablePath] = []byte("corrupt-helper")
	if _, err = manager.Ensure(t.Context(), helper.ID, helper.Platform); err == nil {
		t.Fatal("HTTP mirror revalidation accepted an altered installed helper")
	}
}

func TestHTTPHelperDownloaderBoundsAndFailures(t *testing.T) {
	for _, test := range []struct {
		name      string
		code      int
		body      string
		chunked   bool
		wantError bool
	}{
		{name: "exact-bound", code: 200, body: "1234"},
		{name: "content-length-overflow", code: 200, body: "12345", wantError: true},
		{name: "chunked-overflow", code: 200, body: "12345", chunked: true, wantError: true},
		{name: "server-error", code: 503, body: "no", wantError: true},
		{name: "missing-release", code: 404, body: "no", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.code)
				if test.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			raw, err := (HTTPHelperDownloader{Client: server.Client()}).Fetch(t.Context(), server.URL, 4)
			if (err != nil) != test.wantError {
				t.Fatalf("error=%v expected error=%t", err, test.wantError)
			}
			if !test.wantError && string(raw) != test.body {
				t.Fatal("HTTP download changed bytes")
			}
		})
	}
}

func TestHTTPHelperDownloaderCancellationClosesRequest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err := (HTTPHelperDownloader{Client: server.Client()}).Fetch(ctx, server.URL, 1024)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request did not stop at its deadline: %v", err)
	}
}

func TestHTTPHelperDownloaderRejectsInvalidBoundBeforeRequest(t *testing.T) {
	for _, limit := range []int64{-1, 0, maxRuntimeHelperBytes + 2} {
		if _, err := (HTTPHelperDownloader{}).Fetch(t.Context(), "https://example.invalid/", limit); err == nil ||
			!strings.Contains(err.Error(), "bound is invalid") {
			t.Fatalf("invalid bound %d reached the network: %v", limit, err)
		}
	}
}
