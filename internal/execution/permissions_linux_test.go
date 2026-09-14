//go:build linux

package execution

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

func TestLinuxRunnerCanWriteStateButCannotReadVaultKey(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to execute the probe as an unprivileged UID")
	}
	root, err := os.MkdirTemp("/tmp", "loki-execution-permissions-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	vault := filepath.Join(root, "vault")
	runner := filepath.Join(root, "runner")
	if err := os.Mkdir(vault, 0700); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(vault, "master.key")
	if err := os.WriteFile(key, []byte("synthetic-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runner, 0700); err != nil {
		t.Fatal(err)
	}
	const runnerUID = 65534
	if err := os.Chown(runner, runnerUID, runnerUID); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", `set -e; printf ok > "$1/probe"; test ! -r "$2"`, "probe", runner, key)
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: runnerUID, Gid: runnerUID, NoSetGroups: true}}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("runner permission probe: %v: %s", err, output)
	}
	if raw, err := os.ReadFile(filepath.Join(runner, "probe")); err != nil || string(raw) != "ok" {
		t.Fatalf("runner state probe = %q, %v", raw, err)
	}
}
