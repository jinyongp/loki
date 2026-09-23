package bootstrap

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"loki/internal/host/releases"
)

type ReleaseBindingInfo struct {
	ReleaseTag            string `json:"release_tag"`
	ReleaseManifestSHA256 string `json:"release_manifest_sha256"`
	HostBinarySHA256      string `json:"host_binary_sha256"`
}

func DecodeEmbeddedRelease(tag, manifestBase64 string) ([]byte, ReleaseBindingInfo, error) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, ReleaseBindingInfo{}, errors.New("bootstrap release tag is not embedded")
	}
	manifestRaw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(manifestBase64))
	if err != nil || len(manifestRaw) == 0 {
		return nil, ReleaseBindingInfo{}, errors.New("bootstrap release manifest is not embedded")
	}
	manifest, err := releases.LoadReleaseManifest(manifestRaw)
	if err != nil {
		return nil, ReleaseBindingInfo{}, fmt.Errorf("bootstrap release manifest is invalid: %w", err)
	}
	if tag != "v"+manifest.Generation.Spec.Version {
		return nil, ReleaseBindingInfo{}, errors.New("bootstrap release tag does not match embedded manifest")
	}
	sum := sha256.Sum256(manifestRaw)
	return append([]byte(nil), manifestRaw...), ReleaseBindingInfo{
		ReleaseTag:            tag,
		ReleaseManifestSHA256: hex.EncodeToString(sum[:]),
		HostBinarySHA256:      manifest.HostBinary.SHA256,
	}, nil
}
