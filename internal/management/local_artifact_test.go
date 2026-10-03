package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"loki/internal/tools"
)

func TestOfflineAcquisitionKeepsTrustedIdentityAndCancellation(t *testing.T) {
	data := []byte("receipt-bound archive bytes")
	artifact := tools.Artifact{Module: "browser", Release: "0.2.0", Target: tools.Target{OS: "linux", Arch: "amd64", Mode: tools.ProjectHost}, URL: "https://example.org/browser.zip", SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Bytes: int64(len(data)), Format: "zip"}
	directory := t.TempDir()
	path := filepath.Join(directory, artifact.SHA256+".zip")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := localArtifact(t.Context(), directory, artifact, &output); err != nil || !bytes.Equal(output.Bytes(), data) {
		t.Fatalf("receipt-bound local acquisition failed: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	output.Reset()
	if err := localArtifact(ctx, directory, artifact, &output); err == nil || output.Len() != 0 {
		t.Fatal("canceled local acquisition copied archive data")
	}
	data[0] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := localArtifact(t.Context(), directory, artifact, &output); err == nil {
		t.Fatal("modified local bytes acquired catalog authority")
	}
}
