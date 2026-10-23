//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestApplicationRestoreIdentityFileBoundary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "identity")
	private := []byte("synthetic-operator-identity\n")
	if err := os.WriteFile(path, private, 0600); err != nil {
		t.Fatal(err)
	}
	body, err := readApplicationRestoreIdentity(root, path)
	if err != nil || !bytes.Equal(body, private) {
		t.Fatal("private file did not preserve exact bytes", err)
	}
	clear(body)
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := readApplicationRestoreIdentity(root, path); err == nil {
		t.Fatal("group-readable operator identity accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readApplicationRestoreIdentity(root, alias); err == nil {
		t.Fatal("identity symlink accepted")
	}
	if err := os.Link(path, filepath.Join(root, "hardlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := readApplicationRestoreIdentity(root, path); err == nil {
		t.Fatal("multiply linked identity accepted")
	}
	if _, err := readApplicationRestoreIdentity(filepath.Join(root, "other"), path); err == nil {
		t.Fatal("identity outside original host owner accepted")
	}
}

func TestApplicationRestoreRequiresExplicitPlanAndProtectionInput(t *testing.T) {
	for _, args := range [][]string{
		{"compose", "apply", "--state-root", "/tmp/owner", "--plan", "/tmp/plan"},
		{"compose", "apply", "--state-root", "/tmp/owner", "--approve"},
		{"compose", "recover", "--state-root", "/tmp/owner", "--protection", "unlock"},
		{"compose", "status", "--state-root", "/tmp/owner", "--identity-file", "/tmp/owner/key"},
		{"compose", "rollback", "--state-root", "/tmp/owner"},
		{"compose", "prepare", "--state-root", "/tmp/owner", "--backup-id", "backup", "--stdin"},
		{"compose", "prepare-new-deployment", "--state-root", "/tmp/owner", "--backup-id", "backup"},
		{"compose", "prepare", "--state-root", "/tmp/owner", "--backup-id", "backup", "--source-state-root", "/tmp/old-owner"},
	} {
		var out, errout bytes.Buffer
		if code := runHostApplicationRestore(args, &out, &errout); code != 2 {
			t.Fatalf("unsafe/incomplete invocation accepted: %v: %d", args, code)
		}
	}
}
