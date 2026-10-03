package toolchain

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFetchBundleVerifiesAndPublishesAtomically(t *testing.T) {
	payload := []byte("toolchain payload")
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(payload) }))
	defer server.Close()
	raw := []byte(strings.ReplaceAll(`{"version":1,"platform":{"id":"ubuntu","version":"24.04","arch":"amd64"},"apt_packages":[{"name":"git","version":"1"}],"artifacts":[{"name":"go","version":"1","filename":"go.tgz","url":"URL","sha256":"SUM","format":"tar.gz","install_path":"/opt/loki/toolchain/go/1","strip_components":1,"links":{"go":"bin/go"}}]}`, "URL", server.URL))
	raw = []byte(strings.ReplaceAll(string(raw), "SUM", sum))
	output := filepath.Join(t.TempDir(), "bundle")
	if err := FetchBundle(context.Background(), server.Client(), raw, output); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"manifest.json", "provenance.json", "SHA256SUMS", filepath.Join("artifacts", "go.tgz")} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := FetchBundle(context.Background(), server.Client(), raw, output); err == nil {
		t.Fatal("existing output accepted")
	}
}

func TestFetchBundleRejectsChecksumMismatchWithoutOutput(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("bad")) }))
	defer server.Close()
	raw := []byte(strings.ReplaceAll(`{"version":1,"platform":{"id":"ubuntu","version":"24.04","arch":"amd64"},"apt_packages":[{"name":"git","version":"1"}],"artifacts":[{"name":"go","version":"1","filename":"go.tgz","url":"URL","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","format":"tar.gz","install_path":"/opt/loki/toolchain/go/1","strip_components":1,"links":{"go":"bin/go"}}]}`, "URL", server.URL))
	output := filepath.Join(t.TempDir(), "bundle")
	if err := FetchBundle(context.Background(), server.Client(), raw, output); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("failed bundle was published")
	}
}

type bundleRoundTripper struct {
	payloads map[string][]byte
	hits     map[string]int
}

func (r *bundleRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	payload, ok := r.payloads[request.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	}
	r.hits[request.URL.String()]++
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(payload)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func TestFetchBundleWithCatalogDeduplicatesManagedArtifacts(t *testing.T) {
	nodePayload := []byte("node archive")
	pythonPayload := []byte("python archive")
	nodeSHA := fmt.Sprintf("%x", sha256.Sum256(nodePayload))
	pythonSHA := fmt.Sprintf("%x", sha256.Sum256(pythonPayload))
	nodeURL := "https://nodejs.org/download/release/v26.9.0/node-v26.9.0-linux-x64.tar.xz"
	pythonURL := "https://github.com/astral-sh/python-build-standalone/releases/download/20260901/cpython-3.14.7+20260901-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"
	manifestRaw := []byte(fmt.Sprintf(
		`{"version":1,"platform":{"id":"ubuntu","version":"24.04","arch":"amd64"},"apt_packages":[{"name":"git","version":"1"}],"artifacts":[{"name":"node","version":"26.9.0","filename":"node-v26.9.0-linux-x64.tar.xz","url":%q,"sha256":%q,"format":"tar.xz","install_path":"/opt/loki/toolchain/node/26.9.0","strip_components":1,"links":{"node":"bin/node"}}]}`,
		nodeURL, nodeSHA,
	))
	catalogRaw := []byte(fmt.Sprintf(
		`{"version":1,"node":[{"version":"26.9.0","url":%q,"sha256":%q}],"python":[{"version":"3.14.7","build":"20260901","url":%q,"sha256":%q}]}`,
		nodeURL, nodeSHA, pythonURL, pythonSHA,
	))
	transport := &bundleRoundTripper{
		payloads: map[string][]byte{nodeURL: nodePayload, pythonURL: pythonPayload},
		hits:     map[string]int{},
	}
	client := &http.Client{Transport: transport}
	output := filepath.Join(t.TempDir(), "bundle")
	if err := FetchBundleWithCatalog(t.Context(), client, manifestRaw, catalogRaw, output); err != nil {
		t.Fatal(err)
	}
	if transport.hits[nodeURL] != 1 || transport.hits[pythonURL] != 1 {
		t.Fatalf("artifact fetch counts = %#v", transport.hits)
	}
	for _, path := range []string{
		"manifest.json", "catalog.json", "provenance.json", "SHA256SUMS",
		filepath.Join("artifacts", "node-v26.9.0-linux-x64.tar.xz"),
		filepath.Join("artifacts", "cpython-3.14.7+20260901-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz"),
	} {
		if _, err := os.Stat(filepath.Join(output, path)); err != nil {
			t.Fatalf("bundle path %s: %v", path, err)
		}
	}
	var provenance Provenance
	raw, err := os.ReadFile(filepath.Join(output, "provenance.json"))
	if err != nil || json.Unmarshal(raw, &provenance) != nil {
		t.Fatalf("provenance = %s, %v", raw, err)
	}
	if provenance.ManifestSHA != digest(manifestRaw) || provenance.CatalogSHA != digest(catalogRaw) ||
		len(provenance.Artifacts) != 2 {
		t.Fatalf("provenance = %#v", provenance)
	}
	checksums, err := os.ReadFile(filepath.Join(output, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		digest(manifestRaw) + "  manifest.json",
		digest(catalogRaw) + "  catalog.json",
		nodeSHA + "  artifacts/node-v26.9.0-linux-x64.tar.xz",
		pythonSHA + "  artifacts/cpython-3.14.7+20260901-x86_64-unknown-linux-gnu-install_only_stripped.tar.gz",
	} {
		if !strings.Contains(string(checksums), want) {
			t.Fatalf("SHA256SUMS missing %q: %s", want, checksums)
		}
	}
}

func TestFetchBundleWithCatalogRejectsConflictingDuplicateIdentity(t *testing.T) {
	nodePayload := []byte("node archive")
	nodeSHA := fmt.Sprintf("%x", sha256.Sum256(nodePayload))
	nodeURL := "https://nodejs.org/download/release/v26.9.0/node-v26.9.0-linux-x64.tar.xz"
	manifestRaw := []byte(fmt.Sprintf(
		`{"version":1,"platform":{"id":"ubuntu","version":"24.04","arch":"amd64"},"apt_packages":[{"name":"git","version":"1"}],"artifacts":[{"name":"node","version":"26.9.0","filename":"node-v26.9.0-linux-x64.tar.xz","url":%q,"sha256":%q,"format":"tar.xz","install_path":"/opt/loki/toolchain/node/26.9.0","strip_components":1,"links":{"node":"bin/node"}}]}`,
		nodeURL, nodeSHA,
	))
	catalogRaw := []byte(fmt.Sprintf(
		`{"version":1,"node":[{"version":"26.9.0","url":%q,"sha256":%q}]}`,
		nodeURL, strings.Repeat("f", 64),
	))
	output := filepath.Join(t.TempDir(), "bundle")
	if err := FetchBundleWithCatalog(t.Context(), &http.Client{}, manifestRaw, catalogRaw, output); err == nil ||
		!strings.Contains(err.Error(), "conflicts with base manifest") {
		t.Fatalf("conflicting catalog identity error = %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("conflicting bundle was published: %v", err)
	}
}
