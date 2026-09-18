package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	applauncher "loki/internal/app/launcher"
	"loki/internal/daemon"
	"loki/internal/platform/sandbox"
)

const (
	maxLauncherRunTimeoutSeconds      = 24 * 60 * 60
	maxLauncherResultRetentionSeconds = 60 * 60
	maxLauncherJobs                   = 1024
)

type launcherLayout struct {
	Socket                 string
	SocketGID              int
	ExecutorUID            uint32
	DockerSocket           string
	DockerPeerUID          uint32
	PolicySHA256           string
	Image                  string
	Workspace              string
	Environment            []string
	WorkloadUID            uint32
	WorkloadGID            uint32
	MemoryBytes            int64
	PIDs                   int64
	TmpfsBytes             int64
	RunTimeoutSeconds      int
	ResultRetentionSeconds int
	MaxJobs                int
}

func buildLauncher(layout launcherLayout) (applauncher.Options, error) {
	if !filepath.IsAbs(layout.Socket) || filepath.Clean(layout.Socket) != layout.Socket || layout.Socket == string(filepath.Separator) || layout.SocketGID < 0 {
		return applauncher.Options{}, errors.New("launcher layout requires an absolute clean socket and socket GID")
	}
	if layout.ExecutorUID == 0 {
		return applauncher.Options{}, errors.New("launcher executor UID must be unprivileged")
	}
	if layout.WorkloadUID == 0 || layout.WorkloadGID == 0 || layout.ExecutorUID == layout.WorkloadUID {
		return applauncher.Options{}, errors.New("launcher workload identity must be distinct and unprivileged")
	}
	if layout.RunTimeoutSeconds < 1 || layout.RunTimeoutSeconds > maxLauncherRunTimeoutSeconds {
		return applauncher.Options{}, errors.New("launcher run timeout is outside the supported range")
	}
	if layout.ResultRetentionSeconds < 1 || layout.ResultRetentionSeconds > maxLauncherResultRetentionSeconds {
		return applauncher.Options{}, errors.New("launcher result retention is outside the supported range")
	}
	if layout.MaxJobs < 1 || layout.MaxJobs > maxLauncherJobs {
		return applauncher.Options{}, errors.New("launcher job capacity is outside the supported range")
	}
	policy, err := sandbox.NewPolicy(sandbox.PolicyOptions{
		GenerationSHA256: layout.PolicySHA256,
		Image:            layout.Image,
		Workspace:        layout.Workspace,
		UID:              layout.WorkloadUID,
		GID:              layout.WorkloadGID,
		Environment:      layout.Environment,
		MemoryBytes:      layout.MemoryBytes,
		PIDs:             layout.PIDs,
		TmpfsBytes:       layout.TmpfsBytes,
	})
	if err != nil {
		return applauncher.Options{}, err
	}
	expectedUID := layout.DockerPeerUID
	engine, err := sandbox.NewEngine(sandbox.EngineOptions{
		Socket:      layout.DockerSocket,
		ExpectedUID: &expectedUID,
	})
	if err != nil {
		return applauncher.Options{}, err
	}
	return applauncher.Options{
		Socket:          layout.Socket,
		SocketGID:       layout.SocketGID,
		ExecutorUID:     layout.ExecutorUID,
		Policy:          policy,
		Runner:          engine,
		RunTimeout:      time.Duration(layout.RunTimeoutSeconds) * time.Second,
		ResultRetention: time.Duration(layout.ResultRetentionSeconds) * time.Second,
		MaxJobs:         layout.MaxJobs,
	}, nil
}

func requireLauncherRoot(euid int) error {
	if euid != 0 {
		return errors.New("loki-launcher must run as root")
	}
	return nil
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki-launcher", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layoutPath := flags.String("layout", "", "administrator-owned launcher JSON layout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *layoutPath == "" || !filepath.IsAbs(*layoutPath) {
		fmt.Fprintln(stderr, "loki-launcher requires --layout ABSOLUTE_PATH")
		return 2
	}
	if err := requireLauncherRoot(os.Geteuid()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var layout launcherLayout
	if err := daemon.ReadJSON(*layoutPath, &layout); err != nil {
		fmt.Fprintln(stderr, "invalid launcher layout")
		return 2
	}
	options, err := buildLauncher(layout)
	if err != nil {
		fmt.Fprintln(stderr, "invalid launcher configuration")
		return 2
	}
	options.Ready = func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err = applauncher.Run(ctx, options); err != nil {
		fmt.Fprintln(stderr, "launcher service failed:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}
