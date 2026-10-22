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
	"loki/modules/secrets/application"
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
	MetadataSocket  string                              `json:"metadata_socket,omitempty"`
	MetadataGID     uint32                              `json:"metadata_gid,omitempty"`
	AgentUID        uint32                              `json:"agent_uid,omitempty"`
	Principal       string                              `json:"principal,omitempty"`
	AdminSocket     string                              `json:"admin_socket,omitempty"`
	ApplicationRoot string                              `json:"application_root,omitempty"`
	Inbox           string                              `json:"inbox,omitempty"`
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
	if config.MetadataSocket != "" || config.AdminSocket != "" || config.ApplicationRoot != "" || config.Inbox != "" {
		if config.MetadataSocket == "" || config.AdminSocket == "" || config.ApplicationRoot == "" || config.Inbox == "" || config.AgentUID == 0 || config.AgentUID == config.UID || config.Principal == "" {
			return 2
		}
		values, err := application.New(application.Options{Provider: p, Root: config.ApplicationRoot, Inbox: config.Inbox, DeploymentID: config.DeploymentID, PolicySHA256: config.PolicySHA256, Resources: config.Resources, BaseEnvironment: config.BaseEnvironment})
		if err != nil {
			return 2
		}
		results := make(chan error, 3)
		go func() { results <- service.Serve(ctx) }()
		go func() {
			results <- secretbroker.ServeMetadata(ctx, secretbroker.MetadataOptions{Socket: config.MetadataSocket, AgentUID: config.AgentUID, GroupID: config.MetadataGID, Principal: config.Principal, Values: values})
		}()
		go func() { results <- secretbroker.ServeAdmin(ctx, config.AdminSocket, values) }()
		first := <-results
		wasCanceled := ctx.Err() != nil
		cancel()
		<-results
		<-results
		if first != nil && !wasCanceled {
			return 1
		}
		return 0
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
