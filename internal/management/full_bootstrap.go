package management

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// FullBootstrap runs only in the owned, networkless administrator preparation
// container. Its mounts are the selected named volumes plus the separate
// layout, activation and MCP authentication directories. Paths are finite and
// generated, never user shell fragments. It preserves credential/data files.
type FullBootstrap struct {
	Mounts []FullMount `json:"mounts"`
	Script string      `json:"script"`
}

func (t FullTopology) Bootstrap(hostUID uint32, layoutDirectory, authDirectory string) (FullBootstrap, error) {
	result := FullBootstrap{Mounts: []FullMount{}}
	if len(t.Services) == 0 {
		return result, nil
	}
	if !filepath.IsAbs(layoutDirectory) || !filepath.IsAbs(authDirectory) {
		return result, fmt.Errorf("bootstrap host directories must be absolute")
	}
	if err := realDirectories(layoutDirectory, authDirectory); err != nil {
		return result, err
	}
	if !strings.HasPrefix(t.Owner, "loki-tools-") || len(t.Owner) != 27 {
		return result, fmt.Errorf("invalid full deployment owner")
	}
	unique := map[string]FullMount{}
	for _, service := range t.Services {
		for _, mount := range service.Mounts {
			if mount.Kind == "volume" {
				if !strings.HasPrefix(mount.Source, t.Owner+"-") {
					return result, fmt.Errorf("bootstrap volume is not owned by this host")
				}
				mount.ReadOnly = false
				if previous, exists := unique[mount.Target]; exists && previous != mount {
					return result, fmt.Errorf("bootstrap mount collision")
				}
				unique[mount.Target] = mount
			} else if mount.Target == "/etc/loki/activation" {
				unique["/loki-control"] = FullMount{Kind: "bind", Source: mount.Source, Target: "/loki-control"}
			}
		}
	}
	unique["/loki-layout"] = FullMount{Kind: "bind", Source: layoutDirectory, Target: "/loki-layout"}
	unique["/loki-auth"] = FullMount{Kind: "bind", Source: authDirectory, Target: "/loki-auth"}
	for _, mount := range unique {
		result.Mounts = append(result.Mounts, mount)
	}
	slices.SortFunc(result.Mounts, func(a, b FullMount) int { return strings.Compare(a.Target, b.Target) })
	var script strings.Builder
	script.WriteString("set -eu\n")
	directory := func(path string, uid, gid uint32, mode string) {
		fmt.Fprintf(&script, "test ! -L '%s'\ninstall -d -o %d -g %d -m %s '%s'\n", path, uid, gid, mode, path)
	}
	for _, mount := range result.Mounts {
		if mount.Kind != "volume" {
			continue
		}
		known := false
		for _, target := range fullDataTargets {
			if mount.Target == target {
				known = true
			}
		}
		uid, gid, mode := uint32(0), uint32(0), "0700"
		switch mount.Target {
		case "/workspace":
			uid, gid, mode = 10000, 10001, "2770"
		case "/var/lib/loki/browser":
			uid, gid = 10003, 10003
		case "/var/lib/loki/mcp", "/var/lib/loki/sharing", "/var/lib/loki/runner":
			uid, gid = 10000, 10000
		case "/var/log/loki":
			uid, gid = 10002, 10002
		case "/var/lib/loki/executor":
			uid, gid = 10004, 10004
		case "/var/cache/loki", "/var/lib/loki/toolchains":
			mode = "0755"
		case "/run/loki/runtime", "/run/loki/launcher", "/run/loki/signing", "/run/loki/endpoints":
			known = true
			gid, mode = 10001, "0750"
		case "/run/loki/browser":
			known = true
			uid, gid, mode = 10003, 10001, "0750"
		case "/run/loki/executor":
			known = true
			uid, gid, mode = 10004, 10001, "0750"
		}
		if !known {
			return result, fmt.Errorf("bootstrap path is not in the finite owned layout")
		}
		directory(mount.Target, uid, gid, mode)
		if mount.Target == "/var/lib/loki/runtime" {
			directory(mount.Target+"/ports", 0, 0, "0700")
		}
		if mount.Target == "/var/lib/loki/secrets" {
			directory(mount.Target+"/inbox", 0, 0, "0700")
		}
		if mount.Target == "/var/lib/loki/runner" {
			for _, name := range []string{"state", "config", "gh-config", "data", "xdg-state", "snapshots"} {
				directory(mount.Target+"/"+name, 10000, 10000, "0700")
			}
		}
		if mount.Target == "/var/cache/loki" {
			for _, name := range []string{"runner", "npm", "pnpm", "playwright", "go-build", "go-mod", "pip"} {
				directory(mount.Target+"/"+name, 10000, 10000, "0700")
			}
		}
	}
	// Keep the host user's ownership: atomic management writes inherit the
	// activation directory's setgid group and remain readable to role 10001.
	if _, exists := unique["/loki-control"]; exists {
		fmt.Fprintf(&script, "test ! -L /loki-control\ntest \"$(stat -c %%u /loki-control)\" = %d\ntest -f /loki-control/state.json\ntest ! -L /loki-control/state.json\ntest \"$(stat -c %%u /loki-control/state.json)\" = %d\nchgrp 10001 /loki-control /loki-control/state.json\nchmod 2750 /loki-control\nchmod 0640 /loki-control/state.json\n", hostUID, hostUID)
	}
	fmt.Fprintf(&script, "test ! -L /loki-auth\ntest \"$(stat -c %%u /loki-auth)\" = %d\ntest -f /loki-auth/token\ntest ! -L /loki-auth/token\ntest \"$(stat -c %%u /loki-auth/token)\" = %d\nchgrp 10001 /loki-auth /loki-auth/token\nchmod 2750 /loki-auth\nchmod 0640 /loki-auth/token\n", hostUID, hostUID)
	fmt.Fprintf(&script, "test ! -L /loki-layout\ntest \"$(stat -c %%u /loki-layout)\" = %d\nchgrp 10001 /loki-layout\nchmod 2750 /loki-layout\n", hostUID)
	for _, name := range []string{"loki.toml", "mcp.json", "runtime.json", "launcher.json", "executor.json", "github.toml", "ingress.toml", "signing.gitconfig", "allowed-signers", "signing.pub"} {
		fmt.Fprintf(&script, "if test -e '/loki-layout/%s'; then test -f '/loki-layout/%s'; test ! -L '/loki-layout/%s'; chown 0:10001 '/loki-layout/%s'; chmod 0640 '/loki-layout/%s'; fi\n", name, name, name, name, name)
	}
	result.Script = script.String()
	return result, nil
}
