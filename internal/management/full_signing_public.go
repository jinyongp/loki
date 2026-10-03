package management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// This is the credential-free publication DTO produced by the protected Git
// owner. Git owns key derivation and configuration semantics; the host only
// publishes the finite returned files to their declared consumers.
type fullSigningPublic struct {
	Identity struct {
		Schema      int    `json:"schema"`
		Name        string `json:"name"`
		Email       string `json:"email"`
		PublicKey   string `json:"public_key"`
		Fingerprint string `json:"fingerprint"`
		Keygen      string `json:"keygen"`
	} `json:"identity"`
	GitConfig      string `json:"git_config"`
	AllowedSigners string `json:"allowed_signers"`
}

func decodeFullSigningPublic(data []byte, keygen string) (fullSigningPublic, error) {
	var result fullSigningPublic
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil {
		return result, fmt.Errorf("invalid Git-owned public signing metadata")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || result.Identity.Schema != 1 || result.Identity.Keygen != keygen || !strings.HasPrefix(result.Identity.PublicKey, "ssh-ed25519 ") || strings.ContainsAny(result.Identity.PublicKey, "\r\n") || !strings.HasPrefix(result.Identity.Fingerprint, "SHA256:") || len(result.GitConfig) > 64<<10 || result.GitConfig == "" || result.AllowedSigners == "" || strings.Contains(result.GitConfig, "PRIVATE KEY") || strings.Contains(result.AllowedSigners, "PRIVATE KEY") {
		return result, fmt.Errorf("Git signing presentation differs from its selected owner")
	}
	return result, nil
}
