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

func (t FullTopology) Bootstrap(hostUID uint32, layoutDirectory, authDirectory, deployment string) (FullBootstrap, error) {
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
			} else if service.Name == "secrets" && mount.Kind == "bind" && mount.Target == "/run/loki/secrets" {
				unique[mount.Target] = mount
			}
		}
	}
	if _, application := unique["/run/loki/secrets"]; application {
		// Secret metadata preparation references the installed provider and
		// contract, which live in the immutable core payload namespace.
		core := false
		for _, program := range t.Resources.Plan.Programs {
			if program.Module == "runtime-core" {
				mount := fullModuleMount(program)
				unique[mount.Target] = mount
				core = true
			}
		}
		if !core {
			return result, fmt.Errorf("secret preparation requires its immutable core payload")
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
	for _, name := range []string{"loki.toml", "mcp.json", "runtime.json", "launcher.json", "executor.json", "application-resources.json", "github.toml", "ingress.toml", "signing.gitconfig", "allowed-signers", "signing.pub"} {
		mode := "0640"
		if name == "signing.gitconfig" || name == "allowed-signers" || name == "signing.pub" {
			// These public inputs are mounted individually into jobs whose
			// GID is 10000; no private key enters the workload.
			mode = "0644"
		}
		fmt.Fprintf(&script, "if test -e '/loki-layout/%s'; then test -f '/loki-layout/%s'; test ! -L '/loki-layout/%s'; chown 0:10001 '/loki-layout/%s'; chmod %s '/loki-layout/%s'; fi\n", name, name, name, name, mode, name)
	}
	if _, exists := unique["/var/lib/loki/launcher"]; exists {
		// Prepare the canonical journal before any launcher container exists,
		// so partial service creation can still be stopped under its lock.
		script.WriteString("test ! -L /var/lib/loki/launcher/journal\nif test -e /var/lib/loki/launcher/journal; then test -d /var/lib/loki/launcher/journal; test \"$(stat -c %u /var/lib/loki/launcher/journal)\" = 0; test \"$(stat -c %a /var/lib/loki/launcher/journal)\" = 700; else install -d -o 0 -g 0 -m 0700 /var/lib/loki/launcher/journal; /opt/loki/bin/loki host maintenance status --offline --launcher-layout /loki-layout/launcher.json --state-mount /var/lib/loki/launcher >/dev/null; fi\n")
	}
	if _, exists := unique["/run/loki/secrets"]; exists {
		if !deploymentIDPattern.MatchString(deployment) {
			return result, fmt.Errorf("secret preparation requires the original owned OCI reservation")
		}
		program, err := t.Resources.ServiceProgram("runtime-core", "devtools")
		if err != nil {
			return result, err
		}
		contract, err := t.Resources.ServiceAsset("runtime-core", "execution-contract")
		if err != nil {
			return result, err
		}
		// A newly allocated host tmpfs root is empty and owned by this host
		// operator. Existing root-owned preparation is preserved and validated
		// by the same deployment metadata owner as native systemd.
		fmt.Fprintf(&script, "test ! -L /run/loki/secrets\nif test \"$(stat -c %%u /run/loki/secrets)\" != 0; then test \"$(stat -c %%u /run/loki/secrets)\" = %d; test -z \"$(ls -A /run/loki/secrets)\"; fi\ninstall -d -o 0 -g 0 -m 0755 /run/loki/secrets\n", hostUID)
		args := []string{"/opt/loki/bin/loki", "host", "prepare-secrets", "--uid", "10006", "--gid", "10006", "--agent-uid", "10000", "--metadata-gid", "10001", "--identity", "/var/lib/loki/secrets/deployment.json", "--state", "/var/lib/loki/secrets", "--service-state", "/var/lib/loki/secrets", "--runtime", "/run/loki/secrets", "--devtools", program, "--config", "/loki-layout/loki.toml", "--github-config", "/loki-layout/github.toml", "--execution-contract", contract, "--catalog", "/loki-layout/application-resources.json", "--unit-directory=", "--overlay-directory=", "--job-deployment", deployment}
		for i, arg := range args {
			if i > 0 {
				script.WriteByte(' ')
			}
			script.WriteString("'" + strings.ReplaceAll(arg, "'", "'\\''") + "'")
		}
		script.WriteString(" > /loki-layout/secrets-prepared.json\n")
		fmt.Fprintf(&script, "chown %d /loki-layout/secrets-prepared.json\nchmod 0600 /loki-layout/secrets-prepared.json\n", hostUID)
	}
	result.Script = script.String()
	return result, nil
}
