package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"

	"loki/internal/management"
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
	// Bootstrap grants the service group read access. The bearer must remain
	// a bounded regular file with no world access or group write authority.
	info, err := os.Lstat(connection.TokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() != 64 || info.Mode().Perm()&0037 != 0 {
		return fmt.Errorf("MCP bearer is not an owned private regular file")
	}
	file, err := os.Open(connection.TokenFile)
	if err != nil {
		return fmt.Errorf("private MCP bearer could not be opened")
	}
	opened, statErr := file.Stat()
	if statErr != nil || !os.SameFile(info, opened) {
		file.Close()
		return fmt.Errorf("private MCP bearer changed while opening")
	}
	token, err := io.ReadAll(io.LimitReader(file, 65))
	file.Close()
	defer clear(token)
	if err != nil || len(token) != 64 {
		return fmt.Errorf("private MCP bearer is invalid")
	}
	transport, cleanup, err := toolproxy.LocalHTTPTransport(connection.Endpoint, token)
	if err != nil {
		return err
	}
	defer cleanup()
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
	return toolproxy.Run(ctx, toolproxy.Options{Name: "loki-full", Owner: "full", Version: management.Release, Instructions: "Selected Loki tools on the execution host. Browser sessions belong to this connection and have separate state from the desktop app browser. Workspace paths refer to /workspace on the selected host.", Transport: transport, RootURI: "file:///workspace", Authorize: authorize, Revision: revision, AuthorizeResource: func() error { return authorize("") }, ForwardOwnedResources: true, Stderr: diagnostics})
}
