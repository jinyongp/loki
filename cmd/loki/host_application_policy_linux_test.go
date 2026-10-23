//go:build linux

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loki/modules/execution/jobs"
)

func TestApplicationPolicyRequiresOriginalStoppedJournal(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "journal"), 0700); err != nil {
		t.Fatal(err)
	}
	layout := hostLauncherLayout{StateDirectory: root, MaxJobs: 8, MaxOutputBytes: 4096, ResultRetentionSeconds: 60}
	body, _ := json.Marshal(layout)
	path := filepath.Join(t.TempDir(), "launcher.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	limits := jobs.JournalLimits{MaxRecords: 8, MaxRecordBytes: jobs.JournalRecordBudget(4096), MaxOutputBytes: 4096, Retention: time.Minute}
	j, err := jobs.OpenJournal(filepath.Join(root, "journal"), limits)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	operation := "123e4567-e89b-42d3-a456-426614174485"
	if _, err := j.BeginMaintenance(operation, false); err != nil {
		t.Fatal(err)
	}
	if _, err := openApplicationPolicyJournal(path, root, operation); err == nil {
		t.Fatal("live writer accepted")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openApplicationPolicyJournal(path, root, "123e4567-e89b-42d3-a456-426614174486"); err == nil {
		t.Fatal("foreign host operation accepted")
	}
	owned, err := openApplicationPolicyJournal(path, root, operation)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.Close()
	if _, err := jobs.OpenJournal(filepath.Join(root, "journal"), limits); err == nil {
		t.Fatal("policy publication did not retain original journal lock")
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openApplicationPolicyJournal(path, root, ""); err == nil {
		t.Fatal("full startup crossed another operation's active gate")
	}
	current, err := jobs.OpenJournal(filepath.Join(root, "journal"), limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.EndMaintenance(operation); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	idle, err := openApplicationPolicyJournal(path, root, "")
	if err != nil {
		t.Fatal("selected full startup could not hold idle original journal", err)
	}
	defer idle.Close()
}
