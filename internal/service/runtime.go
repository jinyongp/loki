package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"loki/internal/audit"
	"loki/internal/daemon"
	"loki/internal/devtools"
	"loki/internal/dockerproxy"
	"loki/internal/execution"
	"loki/internal/portguard"
	"loki/internal/process"
	"loki/internal/rpc"
	"loki/internal/secret"
)

// RuntimeOptions is administrator-owned role configuration, never tool input.
type RuntimeOptions struct {
	Socket, StateDirectory, InboxDirectory, AuditPath string
	AgentUID                                          uint32
	SocketGID                                         int
	DevtoolsBinary                                    string
	ExecutionContract                                 string
	Workspace, DockerSocket, SnapshotDirectory        string
	RunnerUID, RunnerGID                              uint32
}

func RunRuntime(ctx context.Context, o RuntimeOptions, ready func() error, onAuditError func(error)) error {
	for _, path := range []string{o.Socket, o.StateDirectory, o.InboxDirectory, o.AuditPath, o.DevtoolsBinary, o.ExecutionContract, o.Workspace, o.DockerSocket, o.SnapshotDirectory} {
		if !filepath.IsAbs(path) {
			return errors.New("runtime role paths must be absolute")
		}
	}
	if o.RunnerUID != o.AgentUID {
		return errors.New("runtime runner identity does not match authorized agent")
	}
	workspace, err := filepath.EvalSymlinks(o.Workspace)
	if err != nil {
		return errors.New("runtime workspace cannot be resolved")
	}
	for _, path := range []string{o.StateDirectory, o.InboxDirectory, filepath.Dir(o.AuditPath)} {
		if err := daemon.PrivateDirectory(path); err != nil {
			return err
		}
	}
	var contract execution.Contract
	if err := daemon.ReadJSON(o.ExecutionContract, &contract); err != nil {
		return fmt.Errorf("read execution contract: %w", err)
	}
	if err := contract.Validate(); err != nil {
		return err
	}
	runnerState := contract.Directories["runner-state"].Path
	runnerCache := contract.Directories["runner-cache"].Path
	runnerTemp := contract.Directories["runner-temp"].Path
	if o.SnapshotDirectory != contract.Directories["snapshots"].Path {
		return errors.New("snapshot directory does not match execution contract")
	}
	runnerDirectories := []string{
		runnerState,
		runnerCache,
		runnerTemp,
		o.SnapshotDirectory,
		contract.Environment["XDG_CONFIG_HOME"],
		contract.Environment["GH_CONFIG_DIR"],
		contract.Environment["XDG_DATA_HOME"],
		contract.Environment["XDG_STATE_HOME"],
		contract.Environment["NPM_CONFIG_CACHE"],
		contract.Environment["npm_config_store_dir"],
		contract.Environment["PLAYWRIGHT_BROWSERS_PATH"],
		contract.Environment["GOCACHE"],
		contract.Environment["GOMODCACHE"],
		contract.Environment["PIP_CACHE_DIR"],
	}
	for _, path := range runnerDirectories {
		if err := daemon.OwnedPrivateDirectory(path, o.RunnerUID, o.RunnerGID); err != nil {
			return fmt.Errorf("validate runner directory %q: %w", path, err)
		}
	}
	environment, err := contract.EnvironmentList()
	if err != nil {
		return err
	}
	controller := secret.Controller{StateDirectory: o.StateDirectory, InboxDirectory: o.InboxDirectory}
	devtoolsClient, err := devtools.NewClient(o.DevtoolsBinary, o.Workspace, environment)
	if err != nil {
		return err
	}
	defer devtoolsClient.Close()
	groups := []uint32{o.RunnerGID}
	if workspaceGroup := uint32(o.SocketGID); workspaceGroup != o.RunnerGID {
		groups = append(groups, workspaceGroup)
	}
	devtoolsClient.Identity = &process.Identity{UID: o.RunnerUID, GID: o.RunnerGID, Groups: groups}
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
		GitHubOperations(controller),
		AuditOperations(log),
		PortOperations(&portguard.Guard{Root: workspace, UID: o.AgentUID}),
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
