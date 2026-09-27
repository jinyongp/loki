package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/host/connect"
)

func TestFetchAssetRequiresExactCatalogBytes(t *testing.T) {
	body := []byte("helper")
	sum := sha256.Sum256(body)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()

	root := t.TempDir()
	asset := connect.Asset{
		SourceURL: server.URL + "/helper.zip", SourceSHA256: hex.EncodeToString(sum[:]),
		SourceLength: int64(len(body)), MirrorAsset: "helper.zip",
	}
	if err := fetchAsset(t.Context(), server.Client(), root, asset); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "helper.zip")); err != nil || string(raw) != "helper" {
		t.Fatalf("unexpected helper bytes %q err=%v", raw, err)
	}

	asset.SourceSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := fetchAsset(t.Context(), server.Client(), t.TempDir(), asset); err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestReadBoundedRegularRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "catalog-link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedRegular(link, 1024); err == nil {
		t.Fatal("symlink catalog was accepted")
	}
}
