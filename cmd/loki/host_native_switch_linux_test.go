//go:build linux

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/modules/execution/jobs"
)

func TestNativeReceiptRejectsChangedExecutableAndUnmanagedSelection(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "opt/loki-go/releases/new")
	if err := os.MkdirAll(filepath.Join(base, "opt/loki/bin"), 0700); err != nil {
		t.Fatal(err)
	}
	program := []byte("fixture executable")
	if err := os.WriteFile(filepath.Join(base, "opt/loki/bin/loki"), program, 0700); err != nil {
		t.Fatal(err)
	}
	receipt := []byte(fmt.Sprintf("%x  opt/loki/bin/loki\n", sha256.Sum256(program)))
	if err := os.WriteFile(filepath.Join(base, "SHA256SUMS"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeReleaseReceipt(root, "new"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "opt/loki/bin/loki"), []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeReleaseReceipt(root, "new"); err == nil {
		t.Fatal("changed binary admitted")
	}
	if err := os.Symlink("releases/../../foreign", filepath.Join(root, "opt/loki-go/current")); err != nil {
		t.Fatal(err)
	}
	if _, err := nativeCurrentRelease(root); err == nil {
		t.Fatal("unmanaged current link admitted")
	}
}

func TestHostNativeSwitchOriginalOperationAndOfflineFence(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_HOST_APP_LAYOUT") == "1" {
			t.Fatal("native root CLI acceptance is required")
		}
		t.Skip("native values suite runs this as root")
	}
	root := t.TempDir()
	base := filepath.Join(root, "opt/loki-go/releases/new")
	if err := os.MkdirAll(filepath.Join(base, "opt/loki/bin"), 0700); err != nil {
		t.Fatal(err)
	}
	program := []byte("fixture executable")
	for path, body := range map[string][]byte{
		"opt/loki/bin/loki": program,
		"SHA256SUMS":        []byte(fmt.Sprintf("%x  opt/loki/bin/loki\n", sha256.Sum256(program))),
	} {
		if err := os.WriteFile(filepath.Join(base, path), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(want int, args ...string) string {
		t.Helper()
		var out, diagnostic bytes.Buffer
		if code := runNativeSwitch(args, &out, &diagnostic); code != want {
			t.Fatalf("%v exit%d want%d: %s", args, code, want, diagnostic.String())
		}
		return out.String()
	}
	args := []string{"begin", "--root", root, "--target", "new", "--interrupt-active-jobs"}
	first := run(0, args...)
	id := strings.Fields(first)[0]
	if len(strings.Fields(first)) != 2 || strings.Fields(first)[1] != "-" {
		t.Fatalf("first install: %q", first)
	}
	if again := run(0, args...); again != first {
		t.Fatal("response loss changed operation")
	}
	run(1, "begin", "--root", root, "--target", "new")
	if err := os.Symlink("releases/new", filepath.Join(root, "opt/loki-go/current")); err != nil {
		t.Fatal(err)
	}
	layout := hostLauncherLayout{StateDirectory: "/var/lib/loki-go/launcher", MaxJobs: 8, MaxOutputBytes: 4096, ResultRetentionSeconds: 60}
	raw, _ := json.Marshal(layout)
	config := filepath.Join(root, "etc/loki-go")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "launcher.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "var/lib/loki-go/launcher/journal")
	if err := os.MkdirAll(journalPath, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := jobs.OpenJournal(journalPath, jobs.JournalLimits{MaxRecords: 8, MaxRecordBytes: jobs.JournalRecordBudget(4096), MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.BeginMaintenance(id, true); err != nil {
		t.Fatal(err)
	}
	// Exclusive canonical ownership prevents the offline helper racing a live
	// launcher. The health checkpoint remains durable while release is pending.
	run(1, "finish", "--root", root, "--operation-id", id)
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	run(0, "finish", "--root", root, "--operation-id", id)
	run(0, "finish", "--root", root, "--operation-id", id)
	j, err = jobs.OpenJournal(journalPath, jobs.JournalLimits{MaxRecords: 8, MaxRecordBytes: jobs.JournalRecordBudget(4096), MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	s, err := j.MaintenanceStatus()
	if err != nil || s == nil || s.Active || s.OperationID != id {
		t.Fatalf("offline release: %+v %v", s, err)
	}
}
