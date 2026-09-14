package service

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"loki/internal/action"
	"loki/internal/audit"
	"loki/internal/config"
	"loki/internal/daemon"
	"loki/internal/devtools"
	"loki/internal/dockerproxy"
	"loki/internal/process"
	"loki/internal/project"
	"loki/internal/rpc"
	"loki/internal/secret"
)

// RuntimeOptions is administrator-owned role configuration, never tool input.
type RuntimeOptions struct {
	Socket, StateDirectory, InboxDirectory, ProjectStateDirectory, AuditPath string
	AgentUID                                                                 uint32
	SocketGID                                                                int
	TaskBinary, TaskHome                                                     string
	DevtoolsBinary, DevtoolsHome                                             string
	Layout                                                                   action.Layout
}

func RunRuntime(ctx context.Context, c config.Config, o RuntimeOptions, ready func() error, onAuditError func(error)) error {
	for _, path := range []string{o.Socket, o.StateDirectory, o.InboxDirectory, o.ProjectStateDirectory, o.AuditPath, o.TaskHome, o.DevtoolsBinary, o.DevtoolsHome, o.Layout.Workspace, o.Layout.Binary} {
		if !filepath.IsAbs(path) {
			return errors.New("runtime role paths must be absolute")
		}
	}
	if o.Layout.RuntimeSocket != o.Socket || o.Layout.UID != o.AgentUID {
		return errors.New("runtime role socket or runner identity does not match action layout")
	}
	for _, path := range []string{o.StateDirectory, o.InboxDirectory, o.ProjectStateDirectory, filepath.Dir(o.AuditPath)} {
		if err := daemon.PrivateDirectory(path); err != nil {
			return err
		}
	}
	projects, err := project.New(o.Layout.Workspace, o.ProjectStateDirectory)
	if err != nil {
		return err
	}
	projects.Runner = o.Layout.Runner
	projects.GroupID = int(o.Layout.GID)
	controller := secret.Controller{StateDirectory: o.StateDirectory, InboxDirectory: o.InboxDirectory, Projects: projects}
	devtoolsClient, err := devtools.NewClient(o.DevtoolsBinary, o.Layout.Workspace, devtoolsEnvironment(o.DevtoolsBinary, o.DevtoolsHome))
	if err != nil {
		return err
	}
	devtoolsClient.Identity = &process.Identity{UID: o.Layout.UID, GID: o.Layout.GID, Groups: []uint32{o.Layout.GID}}
	devtoolsBroker := devtools.Broker{Client: devtoolsClient, Secrets: controller}
	o.Layout.MaxProfileProcesses = c.MaxActionProcessesPerProfile
	o.Layout.PreviewBaseDomain = c.PreviewBaseDomain
	runtime, err := action.NewRuntime(controller, o.Layout, process.ManagerOptions{MaxProcesses: c.MaxActionProcesses, MaxOutputBytes: 8 * 1024 * 1024, Retention: time.Duration(c.ProcessRetentionSeconds) * time.Second})
	if err != nil {
		return err
	}
	defer runtime.Close()
	log := &audit.Log{Path: o.AuditPath}
	ops := SecretOperations(controller)
	for _, group := range []map[string]rpc.Operation{
		DevtoolsOperations(devtoolsBroker),
		ProjectStateOperations(projects, project.Tasks{Store: projects, Binary: o.TaskBinary, Home: o.TaskHome}),
		ActionOperations(runtime), AuditOperations(log),
		DockerOperations(dockerproxy.Inspector{Workspace: o.Layout.Workspace, SnapshotRoot: o.Layout.SnapshotDirectory, Socket: "unix://" + o.Layout.DockerSocket}),
	} {
		for name, op := range group {
			if _, exists := ops[name]; exists {
				return errors.New("duplicate runtime operation: " + name)
			}
			ops[name] = op
		}
	}
	listener, err := daemon.Listen(o.Socket, o.SocketGID)
	if err != nil {
		return err
	}
	defer listener.Close()
	if ready != nil {
		if err = ready(); err != nil {
			return err
		}
	}
	server := rpc.Server{AgentUID: o.AgentUID, Operations: ops, Audit: AuditSink(log, onAuditError)}
	return server.Serve(ctx, listener)
}

func devtoolsEnvironment(binary, home string) []string {
	environment := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"TMPDIR=/tmp",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"PATH=" + filepath.Dir(binary) + ":/usr/bin:/bin",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	return environment
}
