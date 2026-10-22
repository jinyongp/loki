package secretoutput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVendoredSourceMatchesReviewedUpstream(t *testing.T) {
	raw, err := os.ReadFile("provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Commit  string `json:"commit"`
		License string `json:"license"`
		Files   map[string]struct {
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err = json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Commit != "ee1e460261720b50b9e186241abb9b0d6e24e174" || receipt.License != "MIT" || len(receipt.Files) != 4 {
		t.Fatal("unreviewed source provenance")
	}
	for source, expected := range receipt.Files {
		body, err := os.ReadFile(filepath.Base(source))
		if err != nil {
			t.Fatal(err)
		}
		// Only the documented package-name/comment adaptation is permitted.
		text := strings.Replace(string(body), "package secretoutput\n", "package redact\n", 1)
		text = strings.Replace(text, "Package secretoutput ", "Package redact ", 1)
		sum := sha256.Sum256([]byte(text))
		if hex.EncodeToString(sum[:]) != expected.SHA256 {
			t.Fatalf("vendor drift: %s", source)
		}
	}
}
