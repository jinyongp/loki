//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/host/lifecycle"
)

func TestHostMigrationRejectsUnsupportedMetadataWithoutDisclosingInput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "plan.json")
	canary := "synthetic-unexpected-private-input"
	if err := os.WriteFile(path, []byte(`{"value":"`+canary+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var plan lifecycle.SecretMigrationIntent
	if err := readSecretMigrationDocument(path, &plan); err == nil || strings.Contains(err.Error(), canary) {
		t.Fatal("unsupported source value field accepted or disclosed", err)
	}
	link := filepath.Join(root, "linked.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := readSecretMigrationDocument(link, &plan); err == nil {
		t.Fatal("symlinked host migration metadata accepted")
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if err := readSecretMigrationDocument(path, &plan); err == nil {
		t.Fatal("writable host migration metadata accepted")
	}
}

func TestHostMigrationRequiresExactPlanApprovalAndPrivateProtectionInput(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"apply", "--manager-root", root, "--plan", filepath.Join(root, "plan.json")},
		{"apply", "--manager-root", root, "--approve"},
		{"recover", "--manager-root", root, "--protection", "unlock"},
		{"recover", "--manager-root", root, "--stdin"},
		{"recover", "--manager-root", root, "--protection", "automatic", "--stdin"},
		{"status", "--manager-root", root, "--protection", "init", "--stdin"},
	} {
		var output, diagnostic bytes.Buffer
		if code := runHostSecretMigration(args, &output, &diagnostic); code != 2 || output.Len() != 0 {
			t.Fatal("invalid migration approval/protection reached host effects", args, code)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("invalid migration arguments changed the selected host", err)
	}
}
