package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
	"loki/internal/work/jobs"
)

type fakeHostLifecycle struct {
	status       lifecycle.UpdateStatus
	plan         lifecycle.PreparedPlan
	result       lifecycle.ApplyResult
	statusErr    error
	prepareErr   error
	applyErr     error
	applyOptions []lifecycle.ApplyOptions
}

func (f *fakeHostLifecycle) Status(context.Context) (lifecycle.UpdateStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeHostLifecycle) Prepare(context.Context) (lifecycle.PreparedPlan, error) {
	return f.plan, f.prepareErr
}

func (f *fakeHostLifecycle) Apply(_ context.Context, options lifecycle.ApplyOptions) (lifecycle.ApplyResult, error) {
	f.applyOptions = append(f.applyOptions, options)
	return f.result, f.applyErr
}

func TestRunHostUpdateWithRoutesActionsAndEncodesJSON(t *testing.T) {
	manager := &fakeHostLifecycle{
		status: lifecycle.UpdateStatus{UpdateAvailable: true},
		plan: lifecycle.PreparedPlan{
			ID: "sha256:" + strings.Repeat("a", 64),
		},
		result: lifecycle.ApplyResult{
			PlanID: "sha256:" + strings.Repeat("a", 64),
		},
	}
	for action, want := range map[string]string{
		"status":  "\"update_available\":true",
		"prepare": "\"id\":\"sha256:",
		"apply":   "\"plan_id\":\"sha256:",
	} {
		var stdout, stderr bytes.Buffer
		code := runHostUpdateWith(
			t.Context(), manager, action,
			lifecycle.ApplyOptions{InterruptActiveJobs: action == "apply"},
			&stdout, &stderr,
		)
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), want) {
			t.Fatalf("%s = code %d stdout %q stderr %q", action, code, stdout.String(), stderr.String())
		}
	}
	if !reflect.DeepEqual(manager.applyOptions, []lifecycle.ApplyOptions{{InterruptActiveJobs: true}}) {
		t.Fatalf("apply options = %#v", manager.applyOptions)
	}

	var stdout, stderr bytes.Buffer
	if code := runHostUpdateWith(t.Context(), manager, "unknown", lifecycle.ApplyOptions{}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown action code = %d", code)
	}
}

func TestLauncherJournalInventoryReadsActiveJobsWithoutWriterOwnership(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "launcher")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	limits := jobs.JournalLimits{
		MaxRecords: 8, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: time.Minute,
	}
	journal, err := jobs.OpenJournal(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	now := time.Now().UTC()

	runningID := strings.Repeat("a", 32)
	if _, err = journal.Admit(runningID, "oci:"+strings.Repeat("b", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(runningID, "oci-instance-sha256:"+strings.Repeat("1", 64), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkRunning(runningID, now.Add(2*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	doneID := strings.Repeat("c", 32)
	if _, err = journal.Admit(doneID, "oci:"+strings.Repeat("d", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkTerminal(doneID, jobs.Result{
		Outcome: jobs.OutcomeLaunchFailed, Cleanup: jobs.CleanupNotRequired,
	}, now.Add(3*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	cleanupID := strings.Repeat("e", 32)
	if _, err = journal.Admit(cleanupID, "oci:"+strings.Repeat("f", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(cleanupID, "oci-instance-sha256:"+strings.Repeat("2", 64), now.Add(3*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkTerminal(cleanupID, jobs.Result{
		Outcome: jobs.OutcomeLaunchFailed, Cleanup: jobs.CleanupPending,
	}, now.Add(4*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	layoutPath := filepath.Join(t.TempDir(), "launcher.json")
	raw, err := json.Marshal(map[string]any{
		"StateDirectory":         dir,
		"MaxJobs":                limits.MaxRecords,
		"MaxOutputBytes":         limits.MaxOutputBytes,
		"ResultRetentionSeconds": 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(layoutPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(layoutPath, 0600); err != nil {
		t.Fatal(err)
	}

	inventory := launcherJournalInventory{LayoutPath: layoutPath}
	active, err := inventory.ActiveJobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(active, []string{runningID, cleanupID}) {
		t.Fatalf("active jobs = %#v", active)
	}
	if _, err = jobs.OpenJournal(dir, limits); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("inventory disturbed launcher journal ownership: %v", err)
	}
}
