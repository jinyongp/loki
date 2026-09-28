//go:build windows

package windows

import (
	"context"
	"os"
	"os/user"
	"strings"
	"testing"
	"time"
)

func TestWindowsConnectionTaskPrincipalSID(t *testing.T) {
	platform := PowerShellConnectionTaskPlatform{}
	want, err := platform.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{want, current.Username} {
		got, err := resolveConnectionTaskSID(identity)
		if err != nil || got != want {
			t.Fatalf("current-user identity did not resolve to the process SID: %v", err)
		}
	}
	for _, identity := range []string{"", " \t", "invalid\nprincipal"} {
		if _, err := resolveConnectionTaskSID(identity); err == nil {
			t.Fatal("malformed principal accepted")
		}
	}
}

func TestWindowsConnectionTaskNativeRoundTrip(t *testing.T) {
	if os.Getenv("LOKI_WINDOWS_TASK_ACCEPTANCE") != "1" {
		t.Skip("real Task Scheduler mutations require disposable-runner opt-in")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	distribution := "loki-task-test-" + acceptanceSecret(t)[:16]
	paths := FrontendPaths{Binary: executable}
	platform := &acceptanceTaskPlatform{delegate: PowerShellConnectionTaskPlatform{}, paths: paths}
	manager := ConnectionTaskManager{Platform: platform}
	expected := expectedConnectionTask(paths, distribution)
	// The randomized task is created only in this disposable test and never
	// started. Cleanup still uses the exact owned executable/action contract.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := platform.delegate.Remove(cleanupCtx, expected); err != nil {
			t.Errorf("remove test-owned connection task: %v", err)
		}
	})
	if err := manager.Reconcile(ctx, distribution, true); err != nil {
		t.Fatal(err)
	}
	probe, err := platform.Probe(ctx, expected.TaskName)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := platform.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(probe.UserID, "S-") || probe.UserID != principal {
		t.Fatal("Task Scheduler principal is not the current-user SID")
	}
	if err := validateConnectionTaskProbe(probe, expected, principal); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(ctx, distribution, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(ctx, distribution, false); err != nil {
		t.Fatal(err)
	}
	probe, err = platform.Probe(ctx, expected.TaskName)
	if err != nil || probe.Present {
		t.Fatal("disabled test-owned task remained registered")
	}
}
