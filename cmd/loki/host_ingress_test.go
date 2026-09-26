package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

type fakeHostIngressRuntime struct {
	hosts []string
	err   error
	calls int
}

func (f *fakeHostIngressRuntime) SetIngressHosts(_ context.Context, hosts []string) error {
	f.calls++
	f.hosts = append([]string(nil), hosts...)
	return f.err
}

func TestHostIngressApplyCommitsAndRollsBackState(t *testing.T) {
	stateRoot, _, _ := installedHostInfoFixture(t)
	store, err := lifecycle.OpenFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}

	runtime := &fakeHostIngressRuntime{}
	next := []string{"mcp.example.com"}
	if err = applyHostIngressState(t.Context(), store, runtime, nil, next); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Host.IngressHosts, next) || !reflect.DeepEqual(runtime.hosts, next) {
		t.Fatalf("applied ingress state host=%#v runtime=%#v", snapshot.Host.IngressHosts, runtime.hosts)
	}

	runtime.err = errors.New("synthetic ingress runtime failure")
	if err = applyHostIngressState(t.Context(), store, runtime, next, []string{"new.example.com"}); err == nil {
		t.Fatal("runtime ingress failure returned success")
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Host.IngressHosts, next) {
		t.Fatalf("failed ingress update changed durable host state: %#v", snapshot.Host.IngressHosts)
	}
}

func TestHostIngressIdempotentApplyReconcilesRuntimeWithoutRevisionChurn(t *testing.T) {
	stateRoot, _, _ := installedHostInfoFixture(t)
	store, err := lifecycle.OpenFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 26, 7, 0, 0, 0, time.UTC)
	if err = store.CommitIngressHosts(t.Context(), []string{"mcp.example.com"}, now); err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHostIngressRuntime{}
	if err = applyHostIngressState(t.Context(), store, runtime, before.Host.IngressHosts, before.Host.IngressHosts); err != nil {
		t.Fatal(err)
	}
	after, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.calls != 1 || !reflect.DeepEqual(runtime.hosts, []string{"mcp.example.com"}) {
		t.Fatalf("runtime reconcile calls=%d hosts=%#v", runtime.calls, runtime.hosts)
	}
	if after.Host.Revision != before.Host.Revision {
		t.Fatalf("idempotent reconcile changed host revision: %q != %q", after.Host.Revision, before.Host.Revision)
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
