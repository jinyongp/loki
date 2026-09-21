package signing

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRealSSHAgentGitSigning(t *testing.T) {
	root := t.TempDir()
	private := filepath.Join(root, "private.sock")
	key := filepath.Join(root, "key")
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + root, "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=Signing Test", "GIT_AUTHOR_EMAIL=signing@example.test", "GIT_COMMITTER_NAME=Signing Test", "GIT_COMMITTER_EMAIL=signing@example.test"}
	run := func(extra []string, binary string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = root
		cmd.Env = append(append([]string{}, env...), extra...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", binary, err, output)
		}
		return output
	}
	run(nil, "/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	agent := exec.CommandContext(t.Context(), "/usr/bin/ssh-agent", "-D", "-a", private)
	agent.Env = env
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if info, err := os.Stat(private); err == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent socket not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	run([]string{"SSH_AUTH_SOCK=" + private}, "/usr/bin/ssh-add", key)
	public := listener(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	proxy := Proxy{PrivateSocket: private, RunnerUID: uint32(os.Getuid()), AgentUID: uint32(os.Getuid())}
	done := make(chan error, 1)
	go func() { done <- proxy.Serve(ctx, public) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	proxyEnv := []string{"SSH_AUTH_SOCK=" + public.Addr().String()}
	run(proxyEnv, "/usr/bin/ssh-add", "-l")
	deny := exec.CommandContext(t.Context(), "/usr/bin/ssh-add", "-D")
	deny.Env = append(append([]string{}, env...), proxyEnv...)
	if err := deny.Run(); err == nil {
		t.Fatal("proxy allowed identity deletion")
	}
	run(proxyEnv, "/usr/bin/ssh-add", "-l")
	run(nil, "/usr/bin/git", "init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("signed fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(nil, "/usr/bin/git", "add", "file.txt")
	run(proxyEnv, "/usr/bin/git", "-c", "gpg.format=ssh", "-c", "user.signingkey="+key+".pub", "-c", "commit.gpgsign=true", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "signed fixture")
	publicKey, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	signers := filepath.Join(root, "allowed-signers")
	if err := os.WriteFile(signers, append([]byte("signing@example.test "), publicKey...), 0600); err != nil {
		t.Fatal(err)
	}
	run(nil, "/usr/bin/git", "-c", "gpg.ssh.allowedSignersFile="+signers, "verify-commit", "HEAD")
}
