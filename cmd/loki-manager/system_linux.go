package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"loki/internal/management"
)

func systemCommand(ctx context.Context, root string, args []string, input io.Reader, out, diagnostics io.Writer) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "_host-authorization", "_prepare-host-relay":
		if len(args) != 1 {
			return true, fmt.Errorf("invalid protected host request")
		}
		if root == "" {
			var err error
			root, err = management.DefaultRoot()
			if err != nil {
				return true, err
			}
		}
		store := management.Store{Root: root}
		selected, err := store.ExecutionSelection()
		if err != nil {
			return true, err
		}
		if args[0] == "_host-authorization" {
			required := os.Geteuid() != 0 && (selected == nil || !selected.System)
			if required {
				required = exec.CommandContext(ctx, "sudo", "-n", "true").Run() != nil
			}
			return true, json.NewEncoder(out).Encode(map[string]bool{"administrator_required": required})
		}
		password, err := io.ReadAll(io.LimitReader(input, 4097))
		defer clear(password)
		if err != nil || len(password) > 4096 {
			return true, fmt.Errorf("invalid bounded administrator input")
		}
		selected, err = management.PrepareSystemHostPassword(ctx, store, password, diagnostics)
		if err != nil {
			return true, err
		}
		return true, json.NewEncoder(out).Encode(selected)
	case "_system-relay":
		f := flag.NewFlagSet(args[0], flag.ContinueOnError)
		f.SetOutput(diagnostics)
		socket := f.String("socket", "", "owned system socket")
		if err := f.Parse(args[1:]); err != nil {
			return true, err
		}
		return true, management.SystemRelay(ctx, *socket, f.Args(), input, out, diagnostics)
	case "_prepare-system-host", "_system-host", "_remove-system-host":
		f := flag.NewFlagSet(args[0], flag.ContinueOnError)
		f.SetOutput(diagnostics)
		uid := f.Uint64("owner-uid", 0, "login UID")
		socket := f.String("socket", "", "owned system socket")
		if err := f.Parse(args[1:]); err != nil {
			return true, err
		}
		if f.NArg() != 0 || *uid == 0 || *uid > uint64(^uint32(0)) || os.Geteuid() != 0 {
			return true, fmt.Errorf("invalid administrator system-host request")
		}
		selected := management.SystemSelection(uint32(*uid))
		if args[0] == "_remove-system-host" {
			return true, management.RemoveSystemHost(ctx, uint32(*uid), diagnostics)
		}
		binary, err := os.Executable()
		if err != nil {
			return true, err
		}
		binary, err = filepath.EvalSymlinks(binary)
		if err != nil {
			return true, err
		}
		if args[0] == "_prepare-system-host" {
			selected, err := management.InstallSystemHost(ctx, uint32(*uid), binary, diagnostics)
			if err != nil {
				return true, err
			}
			return true, json.NewEncoder(out).Encode(selected)
		}
		if root != selected.Root || binary != selected.Command || *socket != selected.Socket || os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) || os.Getenv("LISTEN_FDS") != "1" {
			return true, fmt.Errorf("system host must be started by its owned socket unit")
		}
		file := os.NewFile(3, "systemd-listener")
		defer file.Close()
		listener, err := net.FileListener(file)
		if err != nil {
			return true, err
		}
		defer listener.Close()
		unix, ok := listener.(*net.UnixListener)
		if !ok {
			return true, fmt.Errorf("system host requires a Unix socket")
		}
		return true, management.ServeSystemHost(ctx, unix, binary, root, uint32(*uid))
	}
	return false, nil
}

func prepareLocalSystemHost(ctx context.Context, store management.Store, input io.Reader, diagnostics io.Writer) (*management.ExecutionSelection, error) {
	return management.PrepareSystemHost(ctx, store, input, diagnostics)
}
