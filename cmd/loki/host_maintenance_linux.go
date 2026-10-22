//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"loki/internal/daemon"
	"loki/internal/fault"
	"loki/modules/execution/jobs"
	"loki/modules/execution/jobs/remote"
)

// Host transaction adapters use this finite bridge inside the launcher
// namespace. It carries operation intent only, never arbitrary commands.
func runHostAdmission(args []string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "maintenance requires the host administrator")
		return 1
	}
	if len(args) == 0 {
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet("host maintenance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("socket", "/run/loki-go/launcher/control.sock", "root-owned launcher channel")
	operation := flags.String("operation-id", "", "original host transaction UUID")
	interrupt := flags.Bool("interrupt", false, "explicitly cancel and verify owned Job cleanup")
	offline := flags.Bool("offline", false, "acknowledge completed cleanup while holding the stopped launcher's journal lock")
	launcherLayout := flags.String("launcher-layout", "", "frozen launcher layout for offline end/status only")
	stateMount := flags.String("state-mount", "", "fixed isolated helper mount: /var/lib/loki/launcher")
	restoreStage := flags.String("restore-stage", "", "private offline launcher restore stage; preserve-history only")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || action != "begin" && action != "end" && action != "status" && action != "preserve-history" || action != "begin" && *interrupt || (action == "status") != (*operation == "") {
		return 2
	}
	if action == "preserve-history" {
		if !*offline || *launcherLayout == "" || *restoreStage == "" || !filepath.IsAbs(*restoreStage) || filepath.Clean(*restoreStage) != *restoreStage || *restoreStage == "/" || *interrupt {
			return 2
		}
		if *stateMount != "" && *stateMount != "/var/lib/loki/launcher" {
			return 2
		}
		if _, err := jobs.JobIDForRequestID(*operation); err != nil {
			return 2
		}
		return runOfflinePreserveHostHistory(*operation, *launcherLayout, *stateMount, *restoreStage, stdout, stderr)
	}
	if *restoreStage != "" {
		return 2
	}
	if *offline {
		if action == "begin" || *launcherLayout == "" {
			return 2
		}
		if action == "end" {
			if _, err := jobs.JobIDForRequestID(*operation); err != nil {
				return 2
			}
		}
		if *stateMount != "" && *stateMount != "/var/lib/loki/launcher" {
			return 2
		}
		return runOfflineHostMaintenance(action, *operation, *launcherLayout, *stateMount, stdout, stderr)
	}
	if *launcherLayout != "" || *stateMount != "" {
		return 2
	}
	client, err := remote.NewHostMaintenance(*socket)
	if err != nil {
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var result any
	switch action {
	case "begin":
		result, err = client.Begin(ctx, *operation, *interrupt)
	case "end":
		result, err = client.End(ctx, *operation)
	case "status":
		result, err = client.Status(ctx)
	}
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 1
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 1
	}
	return 0
}

// This bridge merges request history into an unpublished restore stage while the current
// journal retains the exact original maintenance admission. No history-only
// command grants permission to replace or reactivate the serving launcher.
func runOfflinePreserveHostHistory(operation, layoutPath, stateMount, restoreStage string, stdout, stderr io.Writer) int {
	var layout hostLauncherLayout
	if daemon.ReadJSON(layoutPath, &layout) != nil || !filepath.IsAbs(layout.StateDirectory) || filepath.Clean(layout.StateDirectory) != layout.StateDirectory || layout.StateDirectory == "/" {
		fmt.Fprintln(stderr, "current launcher layout is unavailable")
		return 1
	}
	if stateMount != "" {
		layout.StateDirectory = stateMount
	}
	// Opening a missing current history would silently manufacture a new root
	// incarnation. Disaster recovery requires new deployment/auth identities.
	currentRoot := filepath.Join(layout.StateDirectory, "journal")
	for _, path := range []string{filepath.Join(currentRoot, "requests"), filepath.Join(currentRoot, "maintenance.json")} {
		if _, err := os.Lstat(path); err != nil {
			fmt.Fprintln(stderr, "current request history is missing; isolated disaster recovery is required")
			return 1
		}
	}
	limits := jobs.JournalLimits{MaxRecords: layout.MaxJobs, MaxRecordBytes: jobs.JournalRecordBudget(layout.MaxOutputBytes), MaxOutputBytes: layout.MaxOutputBytes, Retention: time.Duration(layout.ResultRetentionSeconds) * time.Second}
	current, err := jobs.OpenJournal(currentRoot, limits)
	if err != nil {
		fmt.Fprintln(stderr, "current launcher journal is unavailable or still owned")
		return 1
	}
	defer current.Close()
	maintenance, err := current.MaintenanceStatus()
	if err != nil || maintenance == nil || !maintenance.Active || maintenance.OperationID != operation {
		fmt.Fprintln(stderr, "original active restore maintenance admission is required")
		return 1
	}
	stage, err := jobs.OpenJournal(filepath.Join(restoreStage, "journal"), limits)
	if err != nil {
		fmt.Fprintln(stderr, "private launcher restore stage is unavailable or still owned")
		return 1
	}
	defer stage.Close()
	if err := stage.PreserveRequestHistory(current); err != nil {
		fmt.Fprintln(stderr, "request history conflicts, exceeds its budget, or has unresolved cleanup")
		return 1
	}
	if json.NewEncoder(stdout).Encode(map[string]any{"operation_id": operation, "preserved": true}) != nil {
		return 1
	}
	return 0
}

// The exclusive canonical journal lock prevents an offline helper racing a
// live launcher. EndMaintenance also refuses retained, unverified Job cleanup.
func runOfflineHostMaintenance(action, operation, path, stateMount string, stdout, stderr io.Writer) int {
	var layout hostLauncherLayout
	if daemon.ReadJSON(path, &layout) != nil || !filepath.IsAbs(layout.StateDirectory) || filepath.Clean(layout.StateDirectory) != layout.StateDirectory || layout.StateDirectory == "/" {
		fmt.Fprintln(stderr, "stopped launcher layout is unavailable")
		return 1
	}
	if stateMount != "" {
		layout.StateDirectory = stateMount
	}
	journal, err := jobs.OpenJournal(filepath.Join(layout.StateDirectory, "journal"), jobs.JournalLimits{MaxRecords: layout.MaxJobs, MaxRecordBytes: jobs.JournalRecordBudget(layout.MaxOutputBytes), MaxOutputBytes: layout.MaxOutputBytes, Retention: time.Duration(layout.ResultRetentionSeconds) * time.Second})
	if err != nil {
		fmt.Fprintln(stderr, "canonical launcher journal is unavailable or still owned")
		return 1
	}
	defer journal.Close()
	var result any
	if action == "end" {
		result, err = journal.EndMaintenance(operation)
	} else {
		result, err = journal.MaintenanceStatus()
	}
	if err != nil {
		fmt.Fprintln(stderr, "stopped launcher cleanup remains unresolved")
		return 1
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 1
	}
	return 0
}
