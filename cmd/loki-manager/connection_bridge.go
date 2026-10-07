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
	"reflect"

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
	selected, err := store.ExecutionSelection()
	if err != nil {
		return err
	}
	if selected == nil || !selected.Remote() {
		return fmt.Errorf("managed connection bridge requires a selected execution host")
	}
	token, err := connectionBridgeToken(store)
	if err != nil {
		return err
	}
	defer clear(token)
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = store.ManagerExecutable(binary)
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
		current, err := store.ExecutionSelection()
		if err != nil || !reflect.DeepEqual(current, selected) {
			return fmt.Errorf("execution host changed; reconnect the managed connection")
		}
		return nil
	}})
}
