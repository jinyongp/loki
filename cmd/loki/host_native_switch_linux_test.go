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

	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	hostpolicy "loki/internal/host/policy"
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

func TestHostNativeResourcePolicyReplayAndRollback(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_HOST_APP_LAYOUT") == "1" {
			t.Fatal("native root policy acceptance is required")
		}
		t.Skip("native values suite runs this as root")
	}
	root := t.TempDir()
	put := func(relative string, body []byte) {
		t.Helper()
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	program := []byte("fixture executable")
	put("opt/loki-go/releases/current/opt/loki/bin/loki", program)
	put("opt/loki-go/releases/current/SHA256SUMS", []byte(fmt.Sprintf("%x  opt/loki/bin/loki\n", sha256.Sum256(program))))
	if err := os.Symlink("releases/current", filepath.Join(root, "opt/loki-go/current")); err != nil {
		t.Fatal(err)
	}
	c, err := config.Parse([]byte("root = '/workspace'\n"))
	if err != nil {
		t.Fatal(err)
	}
	put("etc/loki-go/config.toml", []byte("root = '/workspace'\n"))
	raw, err := os.ReadFile("../../packaging/images/config/execution-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	put("usr/share/doc/loki/execution-contract.json", raw)
	contract, err := loadExecutionContract(filepath.Join(root, "usr/share/doc/loki/execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	put("etc/loki-go/application-resources.json", []byte(`{"resources":[]}`))
	layout, _ := json.Marshal(hostLauncherLayout{StateDirectory: "/var/lib/loki-go/launcher", MaxJobs: 8, MaxOutputBytes: 4096, ResultRetentionSeconds: 60})
	put("etc/loki-go/launcher.json", layout)
	if err := os.MkdirAll(filepath.Join(root, "var/lib/loki-go/launcher/journal"), 0700); err != nil {
		t.Fatal(err)
	}
	resource := controlpolicy.ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "example", Repository: "repo:example", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none"}
	plan, err := hostpolicy.PrepareApplicationChange(c, contract, nil, hostpolicy.ApplicationChange{Action: "set", Alias: resource.Alias, Resource: &resource})
	if err != nil {
		t.Fatal(err)
	}
	run := func(want int, args ...string) string {
		t.Helper()
		var out, diagnostic bytes.Buffer
		if code := runNativeSwitch(args, &out, &diagnostic); code != want {
			t.Fatalf("%v exit%d want%d: %s", args, code, want, diagnostic.String())
		}
		return out.String()
	}
	for attempt := range 2 {
		if attempt == 1 {
			plan, err = hostpolicy.PrepareApplicationChange(c, contract, plan.Resources, hostpolicy.ApplicationChange{Action: "remove", Alias: resource.Alias})
			if err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := json.Marshal(plan)
		put("plan.json", raw)
		args := []string{"begin", "--root", root, "--target", "current", "--application-plan", filepath.Join(root, "plan.json")}
		first := run(0, args...)
		id := strings.Fields(first)[0]
		run(0, "catalog", "--root", root, "--operation-id", id)
		if again := run(0, args...); again != first {
			t.Fatal("catalog publication changed the original retry identity")
		}
		run(1, "verify", "--root", root, "--operation-id", id)
		finishArgs := []string{"finish", "--root", root, "--operation-id", id}
		policy := plan.Policy
		if attempt == 1 {
			run(0, "catalog", "--root", root, "--operation-id", id, "--restored")
			finishArgs = append(finishArgs, "--restored")
			policy = plan.ExpectedPolicy
		}
		layout, _ := json.Marshal(appvalues.BrokerLayout{PolicySHA256: policy})
		put("run/loki-go/secrets/layout.json", layout)
		run(0, finishArgs...)
		resources, err := hostpolicy.LoadApplications(filepath.Join(root, "etc/loki-go/application-resources.json"))
		if err != nil || len(resources) != 1 || resources[0].ID != resource.ID {
			t.Fatalf("policy application/recovery changed profile identity: %+v %v", resources, err)
		}
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
