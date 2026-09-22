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
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/sandbox"
	"loki/internal/work/jobs"
	"loki/internal/work/toolchains"
)

const (
	maxLauncherRunTimeoutSeconds      = 24 * 60 * 60
	maxLauncherResultRetentionSeconds = 60 * 60
	maxLauncherJobs                   = 1024
	maxLauncherConcurrentJobs         = 256
)

type launcherLayout struct {
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
	MaxConcurrentJobs        int
	MaxOutputBytes           int
}

func buildLauncher(layout launcherLayout) (applauncher.Options, error) {
	if !filepath.IsAbs(layout.Socket) || filepath.Clean(layout.Socket) != layout.Socket || layout.Socket == string(filepath.Separator) || layout.SocketGID < 0 {
		return applauncher.Options{}, errors.New("launcher layout requires an absolute clean socket and socket GID")
	}
	if layout.ExecutorUID == 0 {
		return applauncher.Options{}, errors.New("launcher executor UID must be unprivileged")
	}
	if !filepath.IsAbs(layout.StateDirectory) || filepath.Clean(layout.StateDirectory) != layout.StateDirectory ||
		layout.StateDirectory == string(filepath.Separator) {
		return applauncher.Options{}, errors.New("launcher state directory must be an absolute clean non-root path")
	}
	if layout.WorkloadUID == 0 || layout.WorkloadGID == 0 || layout.ExecutorUID == layout.WorkloadUID {
		return applauncher.Options{}, errors.New("launcher workload identity must be distinct and unprivileged")
	}
	if !filepath.IsAbs(layout.ToolchainStore) || filepath.Clean(layout.ToolchainStore) != layout.ToolchainStore ||
		layout.ToolchainStore == string(filepath.Separator) {
		return applauncher.Options{}, errors.New("launcher toolchain store must be an absolute clean non-root path")
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
	if layout.MaxConcurrentJobs < 1 || layout.MaxConcurrentJobs > maxLauncherConcurrentJobs ||
		layout.MaxConcurrentJobs > layout.MaxJobs {
		return applauncher.Options{}, errors.New("launcher concurrent job capacity is outside the supported range")
	}
	if layout.MaxOutputBytes < 1 || layout.MaxOutputBytes > jobs.MaxOutputBytes {
		return applauncher.Options{}, errors.New("launcher output limit is outside the supported range")
	}
	inputDirectory := filepath.Join(layout.StateDirectory, "run-inputs")
	policy, err := sandbox.NewPolicy(sandbox.PolicyOptions{
		GenerationSHA256: layout.PolicySHA256,
		Image:            layout.Image,
		InputDirectory:   inputDirectory,
		Gateway: sandbox.GatewayPolicyOptions{
			Image: layout.GatewayImage, Binary: layout.GatewayBinary,
			ExecutionContract: layout.GatewayExecutionContract, EgressPolicy: layout.GatewayEgressPolicy,
			ProxyPort: layout.GatewayProxyPort, MemoryBytes: layout.GatewayMemoryBytes,
			PIDs: layout.GatewayPIDs, TmpfsBytes: layout.GatewayTmpfsBytes,
		},
		Workspace:          layout.Workspace,
		ToolchainDirectory: layout.ToolchainStore,
		UID:                layout.WorkloadUID,
		GID:                layout.WorkloadGID,
		Environment:        layout.Environment,
		MemoryBytes:        layout.MemoryBytes,
		PIDs:               layout.PIDs,
		TmpfsBytes:         layout.TmpfsBytes,
	})
	if err != nil {
		return applauncher.Options{}, err
	}
	expectedUID := layout.DockerPeerUID
	engine, err := sandbox.NewEngine(sandbox.EngineOptions{
		Socket:      layout.DockerSocket,
		ExpectedUID: &expectedUID,
		OutputBytes: layout.MaxOutputBytes,
	})
	if err != nil {
		return applauncher.Options{}, err
	}
	return applauncher.Options{
		Socket:            layout.Socket,
		SocketGID:         layout.SocketGID,
		ExecutorUID:       layout.ExecutorUID,
		Policy:            policy,
		Runner:            engine,
		Toolchains:        launcherToolchainResolver{store: toolchain.GenerationStore{Root: layout.ToolchainStore}},
		RunTimeout:        time.Duration(layout.RunTimeoutSeconds) * time.Second,
		MaxConcurrentJobs: layout.MaxConcurrentJobs,
	}, nil
}

func openLauncherJournal(layout launcherLayout) (*jobs.Journal, error) {
	return jobs.OpenJournal(layout.StateDirectory, jobs.JournalLimits{
		MaxRecords:     layout.MaxJobs,
		MaxRecordBytes: int64(layout.MaxOutputBytes)*6 + (64 << 10),
		MaxOutputBytes: layout.MaxOutputBytes,
		Retention:      time.Duration(layout.ResultRetentionSeconds) * time.Second,
	})
}

func requireLauncherRoot(euid int) error {
	if euid != 0 {
		return errors.New("loki-launcher must run as root")
	}
	return nil
}

func resolveLauncherPolicy(layout *launcherLayout, configPath, githubConfigPath, executionContractPath string) error {
	generation, contract, err := hostpolicy.CompileFiles(configPath, githubConfigPath, executionContractPath)
	if err != nil {
		return err
	}
	environment, err := contract.EnvironmentList()
	if err != nil {
		return err
	}
	layout.PolicySHA256 = generation.Digest()
	layout.Environment = environment
	layout.GatewayExecutionContract = executionContractPath
	return nil
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki-launcher", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layoutPath := flags.String("layout", "", "administrator-owned launcher JSON layout")
	configPath := flags.String("config", "", "public Loki TOML configuration")
	githubConfigPath := flags.String("github-config", "", "deployment-provided public GitHub TOML configuration")
	executionContractPath := flags.String("execution-contract", "", "administrator-owned execution contract")
	imageOverride := flags.String("image", "", "immutable workload OCI image override")
	gatewayImageOverride := flags.String("gateway-image", "", "immutable gateway OCI image override")
	workspaceOverride := flags.String("workspace-source", "", "host-visible workspace source override")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *layoutPath == "" || !filepath.IsAbs(*layoutPath) ||
		*configPath == "" || !filepath.IsAbs(*configPath) ||
		*executionContractPath == "" || !filepath.IsAbs(*executionContractPath) ||
		*githubConfigPath != "" && !filepath.IsAbs(*githubConfigPath) {
		fmt.Fprintln(stderr, "loki-launcher requires absolute --layout, --config, and --execution-contract paths")
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
	if *imageOverride != "" {
		layout.Image = *imageOverride
	}
	if *gatewayImageOverride != "" {
		layout.GatewayImage = *gatewayImageOverride
	}
	if *workspaceOverride != "" {
		layout.Workspace = *workspaceOverride
	}
	if err := resolveLauncherPolicy(&layout, *configPath, *githubConfigPath, *executionContractPath); err != nil {
		fmt.Fprintln(stderr, "invalid launcher effective policy")
		return 2
	}
	options, err := buildLauncher(layout)
	if err != nil {
		fmt.Fprintln(stderr, "invalid launcher configuration")
		return 2
	}
	journal, err := openLauncherJournal(layout)
	if err != nil {
		fmt.Fprintln(stderr, "launcher state unavailable")
		return 1
	}
	defer journal.Close()
	if inputDirectory := options.Policy.InputDirectory(); inputDirectory != "" {
		if err = daemon.PrivateDirectory(inputDirectory); err != nil {
			fmt.Fprintln(stderr, "launcher input state unavailable")
			return 1
		}
	}
	options.Journal = journal
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
