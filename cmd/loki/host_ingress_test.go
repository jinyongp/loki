package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

type fakeHostIngressBackend struct {
	fakeHostRuntimeBackend
	activeJobs   []string
	ingressHosts []string
}

func (b *fakeHostIngressBackend) ActiveJobs(context.Context) ([]string, error) {
	return append([]string(nil), b.activeJobs...), nil
}

func (b *fakeHostIngressBackend) SetIngressHosts(_ context.Context, hosts []string) error {
	b.ingressHosts = append([]string(nil), hosts...)
	return nil
}

func TestParseHostIngressMutationOptions(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "state")
	options, err := parseHostIngressOptions("allow", []string{
		"--state-root", stateRoot,
		"--interrupt-active-jobs",
		"mcp.example.com",
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if options.StateRoot != stateRoot || !options.InterruptJobs || options.Host != "mcp.example.com" {
		t.Fatalf("mutation options = %#v", options)
	}
	if _, err = parseHostIngressOptions("list", []string{"--interrupt-active-jobs"}, &bytes.Buffer{}); err == nil {
		t.Fatal("list accepted --interrupt-active-jobs")
	}
}

func TestHostIngressMutationUsesComposeJobInventory(t *testing.T) {
	stateRoot, _, _ := installedHostInfoFixture(t)
	previous := openHostIngressBackend
	defer func() { openHostIngressBackend = previous }()

	backend := &fakeHostIngressBackend{
		fakeHostRuntimeBackend: fakeHostRuntimeBackend{snapshots: map[string]string{}},
		activeJobs:             []string{"job-active"},
	}
	openHostIngressBackend = func(*lifecycle.FileStore) (hostIngressBackend, error) {
		return backend, nil
	}

	var stdout, stderr bytes.Buffer
	code := runHostIngress([]string{"allow", "--state-root", stateRoot, "mcp.example.com"}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "job-active") {
		t.Fatalf("blocked ingress code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	store, err := lifecycle.OpenFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Host.IngressHosts) != 0 || len(backend.ingressHosts) != 0 {
		t.Fatalf("blocked ingress mutation changed state: host=%#v runtime=%#v", snapshot.Host.IngressHosts, backend.ingressHosts)
	}

	stdout.Reset()
	stderr.Reset()
	code = runHostIngress([]string{
		"allow", "--state-root", stateRoot, "--interrupt-active-jobs", "mcp.example.com",
	}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("approved ingress code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Host.IngressHosts, []string{"mcp.example.com"}) ||
		!reflect.DeepEqual(backend.ingressHosts, []string{"mcp.example.com"}) {
		t.Fatalf("approved ingress host=%#v runtime=%#v", snapshot.Host.IngressHosts, backend.ingressHosts)
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
