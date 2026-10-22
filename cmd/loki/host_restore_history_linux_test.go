//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/modules/execution/jobs"
)

func TestOfflineRestoreHistoryRequiresOriginalFenceAndPreservesSpentJob(t *testing.T) {
	root := t.TempDir()
	currentRoot, stageRoot := filepath.Join(root, "current"), filepath.Join(root, "stage")
	layout := hostLauncherLayout{StateDirectory: currentRoot, MaxJobs: 16, MaxConcurrentJobs: 8, MaxOutputBytes: 1024, ResultRetentionSeconds: 3600}
	encoded, _ := json.Marshal(layout)
	layoutPath := filepath.Join(root, "launcher.json")
	if err := os.WriteFile(layoutPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	limits := jobs.JournalLimits{MaxRecords: 16, MaxRecordBytes: jobs.JournalRecordBudget(1024), MaxOutputBytes: 1024, Retention: time.Hour}
	if err := os.MkdirAll(filepath.Join(currentRoot, "journal"), 0700); err != nil {
		t.Fatal(err)
	}
	current, err := jobs.OpenJournal(filepath.Join(currentRoot, "journal"), limits)
	if err != nil {
		t.Fatal(err)
	}
	request := "123e4567-e89b-42d3-a456-426614174591"
	job, _ := jobs.JobIDForRequestID(request)
	backend, digest, deployment := "oci:"+strings.Repeat("c", 64)+":"+strings.Repeat("d", 64), strings.Repeat("e", 64), "deployment-"+strings.Repeat("b", 32)
	now := time.Now().UTC()
	if _, _, err := current.AdmitProtectedRequest(job, backend, request, digest, jobs.NetworkNone, nil, nil, deployment, "fixture", "common", now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := current.MarkTerminal(job, jobs.Result{Outcome: jobs.OutcomeLaunchFailed, Cleanup: jobs.CleanupNotRequired}, now); err != nil {
		t.Fatal(err)
	}
	operation := "123e4567-e89b-42d3-a456-426614174592"
	if _, err := current.BeginMaintenance(operation, false); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	if code := runOfflinePreserveHostHistory("123e4567-e89b-42d3-a456-426614174593", layoutPath, "", stageRoot, &output, &diagnostic); code != 1 {
		t.Fatal("unrelated host operation preserved/reopened a restore stage", code)
	}
	if _, err := os.Lstat(stageRoot); !os.IsNotExist(err) {
		t.Fatal("unrelated host operation touched the stage", err)
	}
	if err := os.MkdirAll(filepath.Join(stageRoot, "journal"), 0700); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		output.Reset()
		diagnostic.Reset()
		if code := runOfflinePreserveHostHistory(operation, layoutPath, "", stageRoot, &output, &diagnostic); code != 0 || diagnostic.Len() != 0 {
			t.Fatal("original offline history preservation/retry", code, diagnostic.String())
		}
	}
	stage, err := jobs.OpenJournal(filepath.Join(stageRoot, "journal"), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if _, found, err := stage.Get(job); err != nil || found {
		t.Fatal("history bridge restored an execution record", err)
	}
	if _, err := stage.EndMaintenance(operation); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stage.AdmitProtectedRequest(job, backend, request, digest, jobs.NetworkNone, nil, nil, deployment, "fixture", "common", now.Add(time.Minute), now); !errors.Is(err, jobs.ErrDeliveryUnknown) {
		t.Fatal("restored history allowed another original Job attempt", err)
	}
	current, err = jobs.OpenJournal(filepath.Join(currentRoot, "journal"), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	fence, err := current.MaintenanceStatus()
	if err != nil || fence == nil || !fence.Active || fence.OperationID != operation {
		t.Fatal("stage disposition altered the current host's fence", err)
	}
}

func TestOfflineRestoreHistoryDoesNotManufactureMissingCurrentIncarnation(t *testing.T) {
	root := t.TempDir()
	current, stage := filepath.Join(root, "missing-current"), filepath.Join(root, "stage")
	layoutPath := filepath.Join(root, "launcher.json")
	body, _ := json.Marshal(hostLauncherLayout{StateDirectory: current, MaxJobs: 16, MaxOutputBytes: 1024, ResultRetentionSeconds: 3600})
	if err := os.WriteFile(layoutPath, body, 0600); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	if code := runOfflinePreserveHostHistory("123e4567-e89b-42d3-a456-426614174592", layoutPath, "", stage, &output, &diagnostic); code != 1 {
		t.Fatal("missing current history was treated as a fresh existing incarnation", code)
	}
	for _, path := range []string{current, stage} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("history bridge manufactured missing current/staged roots", err)
		}
	}
}
