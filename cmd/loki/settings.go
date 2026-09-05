package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/config"
	"loki/internal/fault"
)

func runSettings(args []string, stdout, stderr io.Writer) int {
	path := "/etc/loki/config.toml"
	if len(args) > 0 && args[0] == "config" && os.Getenv("LOKI_MCP_CONFIG") != "" {
		path = os.Getenv("LOKI_MCP_CONFIG")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return executeSettings(ctx, args, path, os.Geteuid() == 0, func(unit string) error {
		command := exec.CommandContext(ctx, "/usr/bin/systemctl", "restart", unit)
		command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
		return command.Run()
	}, stdout, stderr)
}

func executeSettings(ctx context.Context, args []string, path string, administrative bool, restart func(string) error, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "usage: loki config show|set NAME VALUE; loki policy list|allow NAME PATH|deny NAME")
		return 2
	}
	command := args[0] + " " + args[1]
	counts := map[string]int{"config show": 2, "config set": 4, "policy list": 2, "policy allow": 4, "policy deny": 3}
	if counts[command] != len(args) {
		fmt.Fprintln(stderr, "invalid settings command arguments")
		return 2
	}
	write := command != "config show" && command != "policy list"
	if write && !administrative {
		fmt.Fprintln(stderr, "changing Loki configuration requires sudo")
		return 1
	}
	var parsed config.Config
	var err error
	if !write {
		parsed, err = config.Load(path)
		if os.IsNotExist(err) && command == "config show" {
			parsed, err = config.Parse(nil)
		}
	} else {
		parsed, err = config.Edit(ctx, path, func(source []byte) ([]byte, error) {
			if command == "config set" {
				value, err := strconv.Atoi(args[3])
				if err != nil {
					return nil, fault.Error("setting value must be an integer")
				}
				return config.RenderProcessLimit(source, args[2], value)
			}
			current, err := config.Parse(source)
			if err != nil {
				return nil, err
			}
			if command == "policy deny" {
				if _, ok := current.Executables[args[2]]; !ok {
					return nil, fault.Error("executable override not found")
				}
				delete(current.Executables, args[2])
			} else {
				if !config.NamePattern.MatchString(args[2]) || !filepath.IsAbs(args[3]) {
					return nil, fault.Error("name and executable path are invalid")
				}
				target, err := filepath.EvalSymlinks(args[3])
				if err != nil {
					return nil, err
				}
				info, err := os.Stat(target)
				if err != nil {
					return nil, err
				}
				if !info.Mode().IsRegular() || unix.Access(target, unix.X_OK) != nil {
					return nil, fault.Error("executable path must be a runnable regular file")
				}
				current.Executables[args[2]] = target
			}
			return config.RenderExecutables(source, current.Executables)
		})
	}
	if err != nil {
		fmt.Fprintln(stderr, "configuration update failed:", fault.Public(err))
		return 1
	}
	if write {
		unit := "loki-mcp.service"
		if command == "config set" {
			unit = "loki-runtime.service"
		}
		if err = restart(unit); err != nil {
			fmt.Fprintln(stderr, "configuration saved; service restart failed:", unit)
			return 1
		}
	}
	if args[0] == "config" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err = encoder.Encode(map[string]int{"max_action_processes": parsed.MaxActionProcesses, "max_action_processes_per_profile": parsed.MaxActionProcessesPerProfile}); err != nil {
			return 1
		}
	} else if command == "policy list" {
		names := make([]string, 0, len(parsed.Executables))
		for name := range parsed.Executables {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if _, err = fmt.Fprintf(stdout, "%s\t%s\n", name, parsed.Executables[name]); err != nil {
				return 1
			}
		}
	}
	return 0
}
