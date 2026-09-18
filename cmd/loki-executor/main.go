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

	appexecutor "loki/internal/app/executor"
	"loki/internal/daemon"
	"loki/internal/work/jobs"
	"loki/internal/work/jobs/remote"
)

const maxExecutorRunTimeoutSeconds = 24 * 60 * 60

type executorLayout struct {
	Socket            string
	SocketGID         int
	AgentUID          uint32
	ExecutorUID       uint32
	LauncherSocket    string
	LauncherUID       uint32
	PolicySHA256      string
	RunTimeoutSeconds int
}

func buildExecutor(layout executorLayout) (appexecutor.Options, error) {
	if !filepath.IsAbs(layout.Socket) || filepath.Clean(layout.Socket) != layout.Socket ||
		layout.Socket == string(filepath.Separator) || layout.SocketGID < 0 {
		return appexecutor.Options{}, errors.New("executor layout requires an absolute clean socket and socket GID")
	}
	if !filepath.IsAbs(layout.LauncherSocket) || filepath.Clean(layout.LauncherSocket) != layout.LauncherSocket ||
		layout.LauncherSocket == string(filepath.Separator) {
		return appexecutor.Options{}, errors.New("executor layout requires an absolute clean launcher socket")
	}
	if layout.AgentUID == 0 || layout.ExecutorUID == 0 || layout.AgentUID == layout.ExecutorUID {
		return appexecutor.Options{}, errors.New("executor agent and executor identities must be distinct and unprivileged")
	}
	if layout.LauncherUID != 0 {
		return appexecutor.Options{}, errors.New("executor launcher UID does not match the privileged launcher role")
	}
	if layout.RunTimeoutSeconds < 1 || layout.RunTimeoutSeconds > maxExecutorRunTimeoutSeconds {
		return appexecutor.Options{}, errors.New("executor run timeout is outside the supported range")
	}
	timeout := time.Duration(layout.RunTimeoutSeconds) * time.Second
	expectedLauncherUID := layout.LauncherUID
	launcher, err := remote.New(remote.Options{
		Socket:       layout.LauncherSocket,
		ExpectedUID:  &expectedLauncherUID,
		PolicySHA256: layout.PolicySHA256,
		Timeout:      timeout,
	})
	if err != nil {
		return appexecutor.Options{}, err
	}
	service, err := jobs.NewService(launcher, jobs.RandomID)
	if err != nil {
		return appexecutor.Options{}, err
	}
	return appexecutor.Options{
		Socket:     layout.Socket,
		SocketGID:  layout.SocketGID,
		AgentUID:   layout.AgentUID,
		Runner:     service,
		RunTimeout: timeout,
	}, nil
}

func requireExecutorIdentity(euid int, configured uint32) error {
	if euid <= 0 || uint32(euid) != configured {
		return errors.New("loki-executor must run as its configured non-root executor UID")
	}
	return nil
}

func run(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki-executor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layoutPath := flags.String("layout", "", "administrator-owned executor JSON layout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *layoutPath == "" || !filepath.IsAbs(*layoutPath) {
		fmt.Fprintln(stderr, "loki-executor requires --layout ABSOLUTE_PATH")
		return 2
	}
	var layout executorLayout
	if err := daemon.ReadJSON(*layoutPath, &layout); err != nil {
		fmt.Fprintln(stderr, "invalid executor layout")
		return 2
	}
	if err := requireExecutorIdentity(os.Geteuid(), layout.ExecutorUID); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options, err := buildExecutor(layout)
	if err != nil {
		fmt.Fprintln(stderr, "invalid executor configuration")
		return 2
	}
	options.Ready = func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err = appexecutor.Run(ctx, options); err != nil {
		fmt.Fprintln(stderr, "executor service failed:", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}
