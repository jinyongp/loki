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
	"loki/internal/control/appvalues"
	"loki/internal/daemon"
	"loki/modules/secrets/application"
	provider "loki/modules/secrets/devtools"
)

type layout = appvalues.BrokerLayout

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
	if secretbroker.RestrictUnixSockets() != nil {
		return 1
	}
	fullRoles := config.MetadataSocket != "" || config.AdminSocket != "" || config.ApplicationRoot != "" || config.Inbox != ""
	if fullRoles && (config.MetadataSocket == "" || config.AdminSocket == "" || config.ApplicationRoot == "" || config.Inbox == "" || config.RuntimeRoot == "" || config.AgentUID == 0 || config.AgentUID == config.UID || config.Principal == "") {
		return 2
	}
	if daemon.PrivateDirectory(config.Root) != nil || daemon.OwnedPrivateDirectory(config.Root, config.UID, uint32(os.Getgid())) != nil {
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if config.RuntimeRoot != "" {
		sockets := []secretbroker.RuntimeSocket{{Path: config.Socket, Group: uint32(os.Getgid())}}
		if config.MetadataSocket != "" {
			sockets = append(sockets, secretbroker.RuntimeSocket{Path: config.MetadataSocket, Group: config.MetadataGID, Public: true}, secretbroker.RuntimeSocket{Path: config.AdminSocket, Group: uint32(os.Getgid())})
		}
		lease, err := secretbroker.AcquireRuntime(config.RuntimeRoot, config.DeploymentID, config.RunnerID, sockets)
		if err != nil {
			return 1
		}
		defer lease.Close()
		config.InstanceID = lease.InstanceID
	}
	verify, stop := context.WithTimeout(ctx, 10*time.Second)
	p, err := provider.New(verify, config.Binary, config.Root)
	stop()
	if err != nil {
		return 1
	}
	ready := make(chan struct{}, 3)
	roleReady := func() error {
		if !fullRoles {
			return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1")
		}
		select {
		case ready <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	jobDeployment := config.JobDeploymentID
	if jobDeployment == "" {
		jobDeployment = config.DeploymentID
	}
	service, err := secretbroker.New(secretbroker.Options{Ready: roleReady, Provider: p, Socket: config.Socket, LauncherUID: config.LauncherUID, DeploymentID: jobDeployment, PolicySHA256: config.PolicySHA256, RunnerID: config.RunnerID, InstanceID: config.InstanceID, Resources: config.Resources, BaseEnvironment: config.BaseEnvironment})
	if err != nil {
		return 2
	}
	if fullRoles {
		values, err := application.New(application.Options{Provider: p, Root: config.ApplicationRoot, Inbox: config.Inbox, DeploymentID: config.DeploymentID, PolicySHA256: config.PolicySHA256, Resources: config.Resources, BaseEnvironment: config.BaseEnvironment})
		if err != nil {
			return 2
		}
		results := make(chan error, 3)
		go func() { results <- service.Serve(ctx) }()
		go func() {
			results <- secretbroker.ServeMetadata(ctx, secretbroker.MetadataOptions{Ready: roleReady, Socket: config.MetadataSocket, AgentUID: config.AgentUID, GroupID: config.MetadataGID, Principal: config.Principal, Values: values})
		}()
		go func() {
			results <- secretbroker.ServeAdmin(ctx, secretbroker.AdminOptions{Ready: roleReady, Socket: config.AdminSocket, Values: values})
		}()
		stopServices := func(count int) {
			cancel()
			for index := 0; index < count; index++ {
				<-results
			}
		}
		for count := 0; count < 3; {
			select {
			case <-ready:
				count++
			case <-results:
				wasCanceled := ctx.Err() != nil
				stopServices(2)
				if wasCanceled {
					return 0
				}
				return 1
			case <-ctx.Done():
				stopServices(3)
				return 0
			}
		}
		if daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") != nil {
			stopServices(3)
			return 1
		}
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
