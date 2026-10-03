package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"

	"loki/internal/management"
	managedcommand "loki/internal/platform/command"
	"loki/internal/transport/toolproxy"
)

func serveFull(ctx context.Context, store management.Store, diagnostics io.Writer) error {
	plan, err := store.PlanFull()
	if err != nil {
		return err
	}
	if len(plan.Enabled) == 0 {
		return fmt.Errorf("enable at least one tool before connecting")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	var releases []func()
	defer func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}()
	for _, program := range plan.Programs {
		installed, ok := state.Installed[program.Module]
		if !ok || installed.Artifact.Identity() != program.Identity {
			return fmt.Errorf("installation changed while connecting; retry")
		}
		release, err := store.Lease(installed.Artifact)
		if err != nil {
			return err
		}
		releases = append(releases, release)
	}
	backend, err := management.NewFullBackend(store, diagnostics)
	if err != nil {
		return err
	}
	connector, ok := backend.(management.FullConnectionBackend)
	if !ok {
		return fmt.Errorf("this full backend does not provide a local MCP connection")
	}
	fmt.Fprintln(diagnostics, "Connecting to the selected full MCP service...")
	connection, err := connector.Connection(ctx)
	if err != nil {
		return err
	}
	if len(connection.Command) < 2 {
		return fmt.Errorf("full connection did not provide its owned stdio adapter")
	}
	command := managedcommand.New(ctx, connection.Command[0], connection.Command[1:]...)
	command.Env = connection.Environment
	command.Stderr = diagnostics
	authorize := func(string) error {
		current, err := store.PlanFull()
		if err != nil || !reflect.DeepEqual(plan, current) {
			return fmt.Errorf("full installation or tool selection changed; reconnect this MCP server")
		}
		return nil
	}
	revision := func() string {
		current, err := store.PlanFull()
		if err != nil {
			return "unavailable"
		}
		data, err := json.Marshal(current)
		if err != nil {
			return "invalid"
		}
		return string(data)
	}
	return toolproxy.Run(ctx, toolproxy.Options{Name: "loki-full", Owner: "full", Version: management.Release, Instructions: "Selected Loki tools on the execution host. Browser sessions belong to this connection and have separate state from the desktop app browser. Workspace paths refer to /workspace on the selected host.", Command: command, RootURI: "file:///workspace", Authorize: authorize, Revision: revision, AuthorizeResource: func() error { return authorize("") }, ForwardOwnedResources: true, Stderr: diagnostics})
}
