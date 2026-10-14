package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"loki/internal/management"
	"loki/internal/transport/toolproxy"
)

func connectionBridgeOrigin(root string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	return fmt.Sprintf("http://127.0.0.1:%d", 19000+binary.BigEndian.Uint32(digest[:4])%20000)
}

func connectionBridgeToken(store management.Store) ([]byte, error) {
	path := filepath.Join(store.Root, "control", "connections", "bridge.token")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != 64 {
		return nil, fmt.Errorf("invalid managed connection bearer")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(string(raw))
	clear(decoded)
	if err != nil {
		return nil, fmt.Errorf("invalid managed connection bearer")
	}
	return raw, nil
}

func runConnectionBridge(ctx context.Context, store management.Store, diagnostics io.Writer) error {
	registry, err := store.HostRegistry()
	if err != nil {
		return err
	}
	return runConnectionBridgeFor(ctx, store, registry.Default, diagnostics)
}

func connectionBridgeScope(store management.Store, h management.HostRecord) management.Store {
	if h.LegacyConnection {
		return store
	}
	return management.Store{Root: filepath.Join(store.Root, "control", "connections", "hosts", h.ID)}
}
func runConnectionBridgeFor(ctx context.Context, frontend management.Store, id string, diagnostics io.Writer) error {
	record, err := frontend.RegisteredHost(id)
	if err != nil {
		return err
	}
	selected := &record.Target
	if !selected.Remote() {
		return fmt.Errorf("connection bridge needs a remote host")
	}
	store := connectionBridgeScope(frontend, record)
	token, err := connectionBridgeToken(store)
	if err != nil {
		return err
	}
	defer clear(token)
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = frontend.ManagerExecutable(binary)
	if err != nil {
		return err
	}
	argv := []string{"--host", selected.Host.Kind, "--remote-command", selected.Command}
	if selected.System {
		argv = append(argv, "--system-socket", selected.Socket)
	}
	if selected.Host.Kind == "wsl" {
		argv = append(argv, "--distribution", selected.Host.Distribution)
	}
	if selected.Host.Kind == "ssh" {
		argv = append(argv, "--address", selected.Host.Address)
	}
	if selected.Root != "" {
		argv = append(argv, "--root", selected.Root)
	}
	if selected.User != "" {
		argv = append(argv, "--remote-user", selected.User)
	}
	argv = append(argv, "tools", "serve")
	return toolproxy.ServeHTTPBridge(ctx, toolproxy.HTTPBridgeOptions{Root: store.Root, Origin: connectionBridgeOrigin(store.Root), Token: token, Version: management.ManagerRelease, Diagnostics: diagnostics, Command: func(ctx context.Context) *exec.Cmd { return newConnectionCommand(ctx, binary, argv...) }, Authorize: func(string) error {
		return authorizeConnectionHost(frontend, record)
	}})
}

func authorizeConnectionHost(frontend management.Store, record management.HostRecord) error {
	current, err := frontend.RegisteredHost(record.ID)
	if err != nil || current.Target != record.Target {
		return fmt.Errorf("bound host changed or was removed; reconnect this connection")
	}
	return nil
}

type connectionBridgeBinding struct {
	ID     string                        `json:"id"`
	Target management.ExecutionSelection `json:"target"`
}

func (b connectionBridgeBinding) matches(record management.HostRecord) bool {
	return b.ID == record.ID && b.Target == record.Target
}
