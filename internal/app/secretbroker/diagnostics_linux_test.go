//go:build linux

package secretbroker

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
	"loki/internal/secretoutput"
	provider "loki/modules/secrets/devtools"
)

func captureProviderDiagnostics(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	return &output
}

func TestProviderDiagnosticsRetainOnlySafeRecovery(t *testing.T) {
	out := captureProviderDiagnostics(t)
	job := strings.Repeat("b", 32)
	for _, err := range []error{
		&provider.Failure{Code: "delivery_outcome_unknown"},
		&provider.Failure{Code: "redaction_unavailable", Limit: &secretoutput.LimitError{Kind: "states", Observed: 262145, Limit: 262144}},
		errors.New("synthetic-private-error /private/provider/root"),
	} {
		reportProviderFailure("deliver", job, err)
	}
	for _, want := range []string{"correlation=" + job, "original Job", "do not prepare or send", "states observed=262145 limit=262144", "keeping output protection enabled"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal("missing operator recovery guidance", want)
		}
	}
	if strings.Contains(out.String(), "synthetic-private") || strings.Contains(out.String(), "/private/") {
		t.Fatal("provider error text escaped into the journal")
	}
	before := out.Len()
	reportProviderFailure("private-operation", job, errors.New("private"))
	reportProviderFailure("deliver", "private-correlation", errors.New("private"))
	if out.Len() != before {
		t.Fatal("unreviewed routing metadata escaped into the journal")
	}
}

func TestRealJobPreparationFailureReportsRecovery(t *testing.T) {
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("pinned provider required")
		}
		t.Skip("native values suite supplies exact provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := provider.New(ctx, binary, filepath.Join(t.TempDir(), "provider"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Protection(ctx, "init", []byte("synthetic-job-protection")); err != nil {
		t.Fatal(err)
	}
	resource := controlpolicy.ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "fixture", Repository: "repository-id", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}}
	s, err := New(Options{Provider: p, LauncherUID: uint32(os.Getuid()) + 1, DeploymentID: "deployment-id", PolicySHA256: strings.Repeat("a", 64), RunnerID: resource.ID, InstanceID: resource.ID, Resources: []controlpolicy.ApplicationResource{resource}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ensureReceiver(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.receiver.Close()
	if _, err := p.Protection(ctx, "lock", nil); err != nil {
		t.Fatal(err)
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var sockets [2]*net.UnixConn
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "private-job-fixture")
		conn, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		sockets[i] = conn.(*net.UnixConn)
		defer sockets[i].Close()
	}
	out := captureProviderDiagnostics(t)
	done := make(chan struct{})
	go func() { defer close(done); s.serveJob(ctx, sockets[0]) }()
	open := privateio.JobOpen{Protocol: 1, JobID: strings.Repeat("b", 32), DeploymentID: s.options.DeploymentID, RequestID: resource.ID, RequestSHA256: strings.Repeat("c", 64), PolicySHA256: s.options.PolicySHA256, Principal: "agent", Repository: resource.Repository, Resource: resource.Alias, Environment: "common", Network: "none", InstanceRef: "fixture-instance", Argv: []string{"fixture"}, CWD: "/", BaseEnvironment: []privateio.EnvironmentEntry{}}
	if err := privateio.WriteControl(sockets[1], privateio.JobRequest{Open: &open}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("failed Job preparation did not finish")
	}
	if !strings.Contains(out.String(), "operation=prepare") || !strings.Contains(out.String(), "correlation="+open.JobID) || !strings.Contains(out.String(), "administrator to unlock") {
		t.Fatal("actual Job preparation discarded provider recovery guidance")
	}
	if strings.Contains(out.String(), "synthetic-job-protection") {
		t.Fatal("credential escaped into operator diagnostics")
	}
}
