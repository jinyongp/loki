// Package public describes Git-owned credential-free signing identities.
package public

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

var signingEmailPattern = regexp.MustCompile(`^[A-Za-z0-9.!$%&'+/=^_` + "`" + `{|}~-]+@[A-Za-z0-9.-]+$`)

type PublicIdentity struct {
	Schema      int    `json:"schema"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	Keygen      string `json:"keygen"`
}

type PublicMaterial struct {
	Identity       PublicIdentity `json:"identity"`
	GitConfig      string         `json:"git_config"`
	AllowedSigners string         `json:"allowed_signers"`
}

func (p PublicIdentity) Material() (PublicMaterial, error) {
	configuration, err := p.GitConfiguration()
	if err != nil {
		return PublicMaterial{}, err
	}
	return PublicMaterial{Identity: p, GitConfig: string(configuration), AllowedSigners: p.Email + " " + p.PublicKey + "\n"}, nil
}

func (p PublicMaterial) Validate() error {
	expected, err := p.Identity.Material()
	if err != nil {
		return err
	}
	if expected.GitConfig != p.GitConfig || expected.AllowedSigners != p.AllowedSigners {
		return fmt.Errorf("public Git signing files differ from their identity")
	}
	return nil
}

func ValidateIdentity(name, email string) error {
	if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) || len(name) > 256 || strings.ContainsAny(name, "\r\n\x00") || len(email) > 320 || !signingEmailPattern.MatchString(email) {
		return fmt.Errorf("Git signing requires a single-line name and an email identity")
	}
	return nil
}

func (p PublicIdentity) Validate() error {
	if p.Schema != 1 || ValidateIdentity(p.Name, p.Email) != nil || !path.IsAbs(p.Keygen) || path.Clean(p.Keygen) != p.Keygen {
		return fmt.Errorf("invalid public Git signing identity")
	}
	fields := strings.Fields(p.PublicKey)
	if len(fields) != 2 || fields[0] != "ssh-ed25519" {
		return fmt.Errorf("Git signing requires an Ed25519 public key")
	}
	key, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil || len(key) != 51 || binary.BigEndian.Uint32(key[:4]) != 11 || string(key[4:15]) != "ssh-ed25519" || binary.BigEndian.Uint32(key[15:19]) != 32 {
		return fmt.Errorf("invalid Ed25519 public key")
	}
	digest := sha256.Sum256(key)
	if p.Fingerprint != "SHA256:"+base64.RawStdEncoding.EncodeToString(digest[:]) {
		return fmt.Errorf("public signing key fingerprint differs from its identity")
	}
	return nil
}

func (p PublicIdentity) GitConfiguration() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return []byte("[user]\n\tname = " + strconv.Quote(p.Name) + "\n\temail = " + strconv.Quote(p.Email) + "\n\tsigningKey = /home/runner/.ssh/id_ed25519.pub\n[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tprogram = " + strconv.Quote(p.Keygen) + "\n\tallowedSignersFile = /etc/loki/allowed-signers\n[commit]\n\tgpgSign = true\n[tag]\n\tgpgSign = true\n\tforceSignAnnotated = true\n"), nil
}
