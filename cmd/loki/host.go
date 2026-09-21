package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"loki/internal/daemon"
	"loki/internal/host/lifecycle"
	"loki/internal/work/jobs"
)

const (
	defaultHostLifecycleRoot = "/var/lib/loki-go/lifecycle"
	defaultLauncherLayout    = "/etc/loki-go/launcher.json"
)

type hostLifecycle interface {
	Status(context.Context) (lifecycle.UpdateStatus, error)
	Prepare(context.Context) (lifecycle.PreparedPlan, error)
	Apply(context.Context, lifecycle.ApplyOptions) (lifecycle.ApplyResult, error)
}

type hostLauncherLayout struct {
	Socket                   string
	SocketGID                int
	ExecutorUID              uint32
	StateDirectory           string
	DockerSocket             string
	DockerPeerUID            uint32
	PolicySHA256             string
	Image                    string
	GatewayImage             string
	GatewayBinary            string
	GatewayExecutionContract string
	GatewayEgressPolicy      string
	GatewayProxyPort         int
	GatewayMemoryBytes       int64
	GatewayPIDs              int64
	GatewayTmpfsBytes        int64
	Workspace                string
	ToolchainStore           string
	Environment              []string
	WorkloadUID              uint32
	WorkloadGID              uint32
	MemoryBytes              int64
	PIDs                     int64
	TmpfsBytes               int64
	RunTimeoutSeconds        int
	ResultRetentionSeconds   int
	MaxJobs                  int
	MaxOutputBytes           int
}

type launcherJournalInventory struct {
	LayoutPath string
}

func (i launcherJournalInventory) ActiveJobs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var layout hostLauncherLayout
	if err := daemon.ReadJSON(i.LayoutPath, &layout); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(layout.StateDirectory) || filepath.Clean(layout.StateDirectory) != layout.StateDirectory ||
		layout.StateDirectory == string(filepath.Separator) ||
		layout.MaxJobs < 1 || layout.MaxOutputBytes < 1 || layout.MaxOutputBytes > jobs.MaxOutputBytes ||
		layout.ResultRetentionSeconds < 1 {
		return nil, errors.New("launcher layout contains invalid job journal settings")
	}
	records, err := jobs.ReadJournalSnapshot(layout.StateDirectory, jobs.JournalLimits{
		MaxRecords:     layout.MaxJobs,
		MaxRecordBytes: int64(layout.MaxOutputBytes)*6 + (64 << 10),
		MaxOutputBytes: layout.MaxOutputBytes,
		Retention:      time.Duration(layout.ResultRetentionSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	active := make([]string, 0, len(records))
	for _, record := range records {
		if record.State == jobs.StateTerminal && record.Result != nil &&
			(record.Result.Cleanup == jobs.CleanupComplete || record.Result.Cleanup == jobs.CleanupNotRequired) {
			continue
		}
		active = append(active, record.ID)
	}
	return active, ctx.Err()
}

type unavailableHostApplier struct{}

func (unavailableHostApplier) Apply(context.Context, lifecycle.ApplyRequest) (lifecycle.ApplyResult, error) {
	return lifecycle.ApplyResult{}, errors.New("host apply transaction engine is not configured")
}

func runHost(args []string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "loki host commands require root")
		return 1
	}
	if len(args) == 0 || args[0] != "update" {
		fmt.Fprintln(stderr, "usage: loki host update status|prepare|apply [OPTIONS]")
		return 2
	}
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: loki host update status|prepare|apply [OPTIONS]")
		return 2
	}
	action := args[1]
	flags := flag.NewFlagSet("host update "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	stateRoot := flags.String("state-root", defaultHostLifecycleRoot, "host lifecycle state root")
	launcherLayout := flags.String("launcher-layout", defaultLauncherLayout, "launcher service layout")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs during apply")
	if flags.Parse(args[2:]) != nil || flags.NArg() != 0 ||
		!filepath.IsAbs(*stateRoot) || filepath.Clean(*stateRoot) != *stateRoot ||
		!filepath.IsAbs(*launcherLayout) || filepath.Clean(*launcherLayout) != *launcherLayout {
		return 2
	}
	if action != "apply" && *interrupt {
		fmt.Fprintln(stderr, "--interrupt-active-jobs is valid only for apply")
		return 2
	}
	store, err := lifecycle.OpenFileStore(*stateRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	manager := lifecycle.Manager{
		Store:   store,
		Jobs:    launcherJournalInventory{LayoutPath: *launcherLayout},
		Applier: unavailableHostApplier{},
	}
	return runHostUpdateWith(context.Background(), manager, action, lifecycle.ApplyOptions{
		InterruptActiveJobs: *interrupt,
	}, stdout, stderr)
}

func runHostUpdateWith(
	ctx context.Context,
	manager hostLifecycle,
	action string,
	options lifecycle.ApplyOptions,
	stdout, stderr io.Writer,
) int {
	if manager == nil {
		fmt.Fprintln(stderr, "host lifecycle manager is not configured")
		return 1
	}
	encoder := json.NewEncoder(stdout)
	switch action {
	case "status":
		status, err := manager.Status(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err = encoder.Encode(status); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "prepare":
		plan, err := manager.Prepare(ctx)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err = encoder.Encode(plan); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "apply":
		result, err := manager.Apply(ctx, options)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err = encoder.Encode(result); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: loki host update status|prepare|apply [OPTIONS]")
		return 2
	}
}
