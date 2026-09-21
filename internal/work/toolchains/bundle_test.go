package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
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
