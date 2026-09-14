package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"loki/internal/audit"
	"loki/internal/daemon"
	"loki/internal/devtools"
	"loki/internal/dockerproxy"
	"loki/internal/process"
	"loki/internal/rpc"
	"loki/internal/secret"
)

// RuntimeOptions is administrator-owned role configuration, never tool input.
type RuntimeOptions struct {
	Socket, StateDirectory, InboxDirectory, AuditPath string
	AgentUID                                          uint32
	SocketGID                                         int
	DevtoolsBinary, DevtoolsHome                      string
	Workspace, DockerSocket, SnapshotDirectory        string
	RunnerUID, RunnerGID                              uint32
}

func RunRuntime(ctx context.Context, o RuntimeOptions, ready func() error, onAuditError func(error)) error {
	for _, path := range []string{o.Socket, o.StateDirectory, o.InboxDirectory, o.AuditPath, o.DevtoolsBinary, o.DevtoolsHome, o.Workspace, o.DockerSocket, o.SnapshotDirectory} {
		if !filepath.IsAbs(path) {
			return errors.New("runtime role paths must be absolute")
		}
	}
	if o.RunnerUID != o.AgentUID {
		return errors.New("runtime runner identity does not match authorized agent")
	}
	for _, path := range []string{o.StateDirectory, o.InboxDirectory, filepath.Dir(o.AuditPath)} {
		if err := daemon.PrivateDirectory(path); err != nil {
			return err
		}
	}
	controller := secret.Controller{StateDirectory: o.StateDirectory, InboxDirectory: o.InboxDirectory}
	devtoolsClient, err := devtools.NewClient(o.DevtoolsBinary, o.Workspace, devtoolsEnvironment(o.DevtoolsBinary, o.DevtoolsHome))
	if err != nil {
		return err
	}
	defer devtoolsClient.Close()
	devtoolsClient.Identity = &process.Identity{UID: o.RunnerUID, GID: o.RunnerGID, Groups: []uint32{o.RunnerGID}}
	devtoolsBroker := devtools.Broker{Client: devtoolsClient, Secrets: controller}
	log := &audit.Log{Path: o.AuditPath}
	ops := SecretOperations(controller)
	ops["status"] = rpc.Operation{Permission: rpc.Agent, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		profiles, err := controller.Profiles(ctx)
		if err != nil {
			return nil, err
		}
		items, ok := profiles["profiles"].([]map[string]any)
		if !ok {
			return nil, errors.New("invalid vault profile response")
		}
		return map[string]any{"initialized": true, "profiles": len(items)}, nil
	}}
	for _, group := range []map[string]rpc.Operation{
		DevtoolsOperations(devtoolsBroker),
		AuditOperations(log),
		DockerOperations(dockerproxy.Inspector{Workspace: o.Workspace, SnapshotRoot: o.SnapshotDirectory, Socket: "unix://" + o.DockerSocket}),
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
