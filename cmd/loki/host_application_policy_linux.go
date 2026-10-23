//go:build linux

package main

import (
	"errors"
	"path/filepath"
	"time"

	"loki/internal/daemon"
	"loki/modules/execution/jobs"
)

func openApplicationPolicyJournal(layoutPath, stateRoot, operation string) (*jobs.Journal, error) {
	var layout hostLauncherLayout
	if daemon.ReadJSON(layoutPath, &layout) != nil || !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot || stateRoot == "/" {
		return nil, errors.New("invalid original launcher")
	}
	j, err := jobs.OpenJournal(filepath.Join(stateRoot, "journal"), jobs.JournalLimits{MaxRecords: layout.MaxJobs, MaxRecordBytes: jobs.JournalRecordBudget(layout.MaxOutputBytes), MaxOutputBytes: layout.MaxOutputBytes, Retention: time.Duration(layout.ResultRetentionSeconds) * time.Second})
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			j.Close()
		}
	}()
	fence, err := j.MaintenanceStatus()
	activeMatches := operation != "" && fence != nil && fence.Active && fence.OperationID == operation
	idleMatches := operation == "" && (fence == nil || !fence.Active)
	if err != nil || !activeMatches && !idleMatches {
		return nil, errors.New("foreign original operation")
	}
	records, err := j.List()
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.State != jobs.StateTerminal || record.Result == nil || record.Result.Cleanup != jobs.CleanupComplete && record.Result.Cleanup != jobs.CleanupNotRequired {
			return nil, errors.New("original cleanup incomplete")
		}
	}
	valid = true
	return j, nil
}
