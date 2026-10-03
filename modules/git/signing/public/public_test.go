package public

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

func publicSigningFixture() PublicIdentity {
	wire := make([]byte, 51)
	binary.BigEndian.PutUint32(wire[:4], 11)
	copy(wire[4:15], "ssh-ed25519")
	binary.BigEndian.PutUint32(wire[15:19], 32)
	for i := 19; i < len(wire); i++ {
		wire[i] = byte(i)
	}
	digest := sha256.Sum256(wire)
	return PublicIdentity{Schema: 1, Name: "Loki Fixture", Email: "fixture@example.com", PublicKey: "ssh-ed25519 " + base64.StdEncoding.EncodeToString(wire), Fingerprint: "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]), Keygen: "/opt/loki/modules/git/native/bin/ssh-keygen"}
}

func TestPublicSigningMaterialRejectsConfigurationAndKeySubstitution(t *testing.T) {
	identity := publicSigningFixture()
	material, err := identity.Material()
	if err != nil {
		t.Fatal(err)
	}
	if err := material.Validate(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(material.GitConfig, "PRIVATE KEY") || !strings.Contains(material.GitConfig, identity.Keygen) {
		t.Fatal("public Git configuration acquired a private key or ambient executable")
	}
	material.GitConfig += "[core]\nsshCommand = foreign-command\n"
	if err := material.Validate(); err == nil {
		t.Fatal("public material accepted an undeclared Git executable")
	}
	material, _ = identity.Material()
	material.Identity.Fingerprint = "SHA256:foreign"
	if err := material.Validate(); err == nil {
		t.Fatal("key identity substitution accepted")
	}
}

func TestSigningIdentityRejectsMultilineAndPrincipalPatterns(t *testing.T) {
	for _, identity := range [][2]string{{"name\n[user]", "fixture@example.com"}, {"name", "*@example.com"}, {"name", "fixture@example.com other-principal"}, {"name", ""}} {
		if ValidateIdentity(identity[0], identity[1]) == nil {
			t.Fatalf("unsafe signing identity accepted: %q", identity)
		}
	}
}
