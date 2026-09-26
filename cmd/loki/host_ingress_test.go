package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

func TestParseHostIngressMutationOptions(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	launcher := filepath.Join(t.TempDir(), "launcher.json")
	options, err := parseHostIngressOptions("allow", []string{
		"--state-root", stateRoot,
		"--launcher-layout", launcher,
		"--interrupt-active-jobs",
		"mcp.example.com",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if options.StateRoot != stateRoot || options.LauncherLayout != launcher ||
		!options.InterruptJobs || options.Host != "mcp.example.com" {
		t.Fatalf("mutation options = %#v", options)
	}
	if _, err = parseHostIngressOptions("list", []string{"--interrupt-active-jobs"}, &bytes.Buffer{}); err == nil {
		t.Fatal("list accepted --interrupt-active-jobs")
	}
}

func TestHostIngressListJSONUsesDurableHostState(t *testing.T) {
	stateRoot, _, _ := installedHostInfoFixture(t)
	store, err := lifecycle.OpenFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	previousNow := lifecycleTimeNow
	lifecycleTimeNow = func() time.Time { return time.Date(2026, 9, 26, 7, 0, 0, 0, time.UTC) }
	defer func() { lifecycleTimeNow = previousNow }()
	if err = store.CommitIngressHosts(t.Context(), []string{"B.example.com", "a.example.com"}, lifecycleTimeNow()); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runHostIngress([]string{"list", "--state-root", filepath.Clean(stateRoot), "--json"}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("list code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var report hostIngressReport
	if err = json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.PublicHosts, []string{"a.example.com", "b.example.com"}) {
		t.Fatalf("ingress report = %#v", report)
	}
}

func TestHostIngressRejectsInvalidHostnameBeforeRuntimeMutation(t *testing.T) {
	options, err := parseHostIngressOptions("allow", []string{"https://bad.example"}, &bytes.Buffer{})
	if err != nil || options.Host != "https://bad.example" {
		t.Fatalf("parse options=%#v err=%v", options, err)
	}
	if _, err = lifecycle.NormalizeIngressHosts([]string{options.Host}); err == nil {
		t.Fatal("invalid ingress hostname was accepted")
	}
}
