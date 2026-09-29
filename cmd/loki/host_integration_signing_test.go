package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/integrations/signing"
)

func TestManagedSigningMaterialGeneratesAndSignsGitCommit(t *testing.T) {
	material, err := prepareManagedSigningMaterial(
		t.Context(), "", "Signing Test", "signing@example.test",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !material.Info.Valid() || len(material.PrivateKey) == 0 {
		t.Fatalf("generated signing material=%#v", material.Info)
	}
	for _, public := range [][]byte{material.PublicInfo, material.PublicKey, material.GitConfig, material.AllowedSigners} {
		if bytes.Contains(public, []byte("PRIVATE KEY")) || bytes.Contains(public, material.PrivateKey) {
			t.Fatal("public signing material leaked private key bytes")
		}
	}

	work := t.TempDir()
	keyPath := filepath.Join(work, "key")
	publicPath := filepath.Join(work, "key.pub")
	signersPath := filepath.Join(work, "allowed-signers")
	if err = os.WriteFile(keyPath, material.PrivateKey, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(publicPath, material.PublicKey, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(signersPath, material.AllowedSigners, 0600); err != nil {
		t.Fatal(err)
	}
	privateSocket := filepath.Join(work, "private.sock")
	publicSocket := filepath.Join(work, "agent.sock")
	agentCtx, cancel := context.WithCancel(t.Context())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- signing.RunAgent(agentCtx, signing.AgentOptions{
			PrivateSocket: privateSocket,
			PublicSocket:  publicSocket,
			Key:           keyPath,
			Grant:         signing.NewSSHSignatureGrant(uint32(os.Getuid())),
			SocketUID:     os.Getuid(),
			SocketGID:     os.Getgid(),
			Ready: func() error {
				close(ready)
				return nil
			},
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("signing agent shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("signing agent did not stop")
		}
	})
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("signing agent exited before ready: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("signing agent did not become ready")
	}

	repo := filepath.Join(work, "repo")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + work,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Signing Test",
		"GIT_AUTHOR_EMAIL=signing@example.test",
		"GIT_COMMITTER_NAME=Signing Test",
		"GIT_COMMITTER_EMAIL=signing@example.test",
		"SSH_AUTH_SOCK=" + publicSocket,
	}
	run := func(binary string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = repo
		cmd.Env = env
		output, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("%s %v: %v %s", binary, args, runErr, output)
		}
		return output
	}
	run("/usr/bin/git", "init", "-q", "--initial-branch=main")
	if err = os.WriteFile(filepath.Join(repo, "file.txt"), []byte("signed fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run("/usr/bin/git", "add", "file.txt")
	run("/usr/bin/git",
		"-c", "gpg.format=ssh",
		"-c", "user.signingKey="+publicPath,
		"-c", "commit.gpgSign=true",
		"-c", "core.hooksPath=/dev/null",
		"commit", "-qm", "signed fixture",
	)
	verify := string(run("/usr/bin/git",
		"-c", "gpg.ssh.allowedSignersFile="+signersPath,
		"verify-commit", "HEAD",
	))
	if !strings.Contains(verify, "Good") && !strings.Contains(verify, "good") {
		t.Fatalf("verify output=%q", verify)
	}
}

func TestManagedSigningMaterialFromStdinBytesMatchesImportedKey(t *testing.T) {
	generated, err := prepareManagedSigningMaterial(t.Context(), "", "Signing Test", "signing@example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(generated.PrivateKey)
	imported, err := prepareManagedSigningMaterialBytes(
		t.Context(), generated.PrivateKey, "Signing Test", "signing@example.test",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(imported.PrivateKey)
	if imported.Info.PublicKey != generated.Info.PublicKey ||
		imported.Info.Fingerprint != generated.Info.Fingerprint ||
		!bytes.Equal(imported.PublicKey, generated.PublicKey) {
		t.Fatalf("stdin signing material=%#v generated=%#v", imported.Info, generated.Info)
	}
}

func TestSigningKeyImportRejectsPublicAndSymlinkFiles(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "key")
	cmd := exec.CommandContext(t.Context(), "/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, output)
	}
	imported, err := prepareManagedSigningMaterial(t.Context(), key, "Signing Test", "signing@example.test")
	if err != nil {
		t.Fatalf("valid signing key import: %v", err)
	}
	if !imported.Info.Valid() || !strings.HasPrefix(imported.Info.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("imported signing material=%#v", imported.Info)
	}
	if err := os.Chmod(key, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSigningImportFile(key); err == nil {
		t.Fatal("public signing key import was accepted")
	}
	if err := os.Chmod(key, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readSigningImportFile(link); err == nil {
		t.Fatal("symlink signing key import was accepted")
	}
}
