//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/app/secretbroker"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	provider "loki/modules/secrets/devtools"
)

type layout struct {
	UID             uint32                              `json:"uid"`
	LauncherUID     uint32                              `json:"launcher_uid"`
	Binary          string                              `json:"binary"`
	Root            string                              `json:"root"`
	Socket          string                              `json:"socket"`
	DeploymentID    string                              `json:"deployment_id"`
	PolicySHA256    string                              `json:"policy_sha256"`
	RunnerID        string                              `json:"runner_id"`
	InstanceID      string                              `json:"instance_id"`
	Resources       []controlpolicy.ApplicationResource `json:"resources"`
	BaseEnvironment map[string]string                   `json:"base_environment"`
}

func run(args []string) int {
	flags := flag.NewFlagSet("loki-secret-broker", flag.ContinueOnError)
	path := flags.String("layout", "", "administrator-owned secret broker layout")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 2
	}
	var config layout
	if daemon.ReadJSON(*path, &config) != nil || config.UID == 0 || config.UID != uint32(os.Geteuid()) || config.LauncherUID == config.UID {
		return 2
	}
	unix.Umask(0077)
	if daemon.PrivateDirectory(config.Root) != nil || daemon.OwnedPrivateDirectory(config.Root, config.UID, uint32(os.Getgid())) != nil {
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	verify, stop := context.WithTimeout(ctx, 10*time.Second)
	p, err := provider.New(verify, config.Binary, config.Root)
	stop()
	if err != nil {
		return 1
	}
	service, err := secretbroker.New(secretbroker.Options{Provider: p, Socket: config.Socket, LauncherUID: config.LauncherUID, DeploymentID: config.DeploymentID, PolicySHA256: config.PolicySHA256, RunnerID: config.RunnerID, InstanceID: config.InstanceID, Resources: config.Resources, BaseEnvironment: config.BaseEnvironment})
	if err != nil {
		return 2
	}
	if err = service.Serve(ctx); err != nil && ctx.Err() == nil {
		return 1
	}
	return 0
}

func main() {
	code := run(os.Args[1:])
	if code != 0 {
		fmt.Fprintln(os.Stderr, "secret broker unavailable")
	}
	os.Exit(code)
}
