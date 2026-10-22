//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"loki/internal/config"
	"loki/internal/daemon"
	hostapplication "loki/internal/host/application"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/safeio"
)

// runHostPrepareSecrets is an installer/unit implementation command. It creates
// deployment metadata and empty directories, never a key or unlock credential.
func runHostPrepareSecrets(args []string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "service preparation requires the host administrator")
		return 1
	}
	flags := flag.NewFlagSet("host prepare-secrets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	uid := flags.Uint64("uid", 0, "allocated dedicated broker UID")
	gid := flags.Uint64("gid", 0, "allocated dedicated broker GID")
	agent := flags.Uint64("agent-uid", 0, "installed agent UID")
	metadataGroup := flags.Uint64("metadata-gid", 0, "installed metadata socket group")
	identity := flags.String("identity", "/etc/loki-go/secrets-deployment.json", "protected host deployment metadata")
	state := flags.String("state", "/var/lib/loki-go/secrets", "host app-generation store")
	serviceState := flags.String("service-state", "/var/lib/loki-go/secrets", "app state path inside the service namespace")
	runtimeRoot := flags.String("runtime", "/run/loki-go/secrets", "tmpfs role directory")
	binary := flags.String("devtools", "/opt/loki/bin/devtools", "immutable provider program inside service namespace")
	configuration := flags.String("config", "/etc/loki-go/config.toml", "effective runtime configuration")
	github := flags.String("github-config", "", "public GitHub configuration fragment")
	contractPath := flags.String("execution-contract", "/usr/share/doc/loki/execution-contract.json", "effective execution contract")
	catalog := flags.String("catalog", "/etc/loki-go/application-resources.json", "host resource catalog")
	unitDirectory := flags.String("unit-directory", "/etc/systemd/system/loki-go-secrets.service.d", "native generation binding drop-in directory; empty disables")
	overlayDirectory := flags.String("overlay-directory", "/etc/loki-go", "native role layouts to bind to this generation; empty disables")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return 2
	}
	for _, id := range []uint64{*uid, *gid, *agent, *metadataGroup} {
		if id == 0 || id > uint64(^uint32(0)) {
			fmt.Fprintln(stderr, "service identities must be nonzero uint32 values")
			return 2
		}
	}
	c, err := config.LoadWithGitHub(*configuration, *github)
	if err != nil {
		fmt.Fprintln(stderr, "effective service configuration is unavailable")
		return 1
	}
	contract, err := loadExecutionContract(*contractPath)
	if err != nil {
		fmt.Fprintln(stderr, "execution contract is unavailable")
		return 1
	}
	resources, err := hostpolicy.LoadApplications(*catalog)
	if err != nil {
		fmt.Fprintln(stderr, "host resource catalog is unavailable")
		return 1
	}
	prepared, err := hostapplication.Prepare(hostapplication.PrepareOptions{UID: uint32(*uid), GID: uint32(*gid), AgentUID: uint32(*agent), MetadataGID: uint32(*metadataGroup), IdentityFile: *identity, StateRoot: *state, ServiceStateRoot: *serviceState, RuntimeRoot: *runtimeRoot, Binary: *binary, Principal: "agent", Configuration: c, Contract: contract, Resources: resources})
	if err != nil {
		fmt.Fprintln(stderr, "secret service metadata or filesystem ownership requires recovery")
		return 1
	}
	if *unitDirectory != "" {
		if !filepath.IsAbs(*unitDirectory) || filepath.Clean(*unitDirectory) != *unitDirectory || strings.ContainsAny(prepared.HostGeneration+*serviceState+*unitDirectory, "\x00\r\n:%") {
			return 2
		}
		if err := os.Mkdir(*unitDirectory, 0755); err != nil && !os.IsExist(err) {
			return 1
		}
		// The generation selector stays host-only; the broker receives precisely
		// this generation at its fixed logical state path, without a symlink.
		body := []byte("[Service]\nBindPaths=" + strconv.Quote(prepared.HostGeneration+":"+*serviceState) + "\n")
		path := filepath.Join(*unitDirectory, "20-generation.conf")
		if safeio.PublishPrivate(path, body, true) != nil || os.Chmod(path, 0644) != nil {
			fmt.Fprintln(stderr, "native generation binding could not be prepared")
			return 1
		}
	}
	if *overlayDirectory != "" {
		owner := sha256.Sum256([]byte(filepath.Clean(*identity)))
		common := map[string]any{"ApplicationCatalog": *catalog}
		changes := map[string]map[string]any{
			"mcp.json":      {"ApplicationCatalog": common["ApplicationCatalog"], "ApplicationSocket": prepared.Layout.MetadataSocket, "ApplicationUID": prepared.Identity.UID},
			"runtime.json":  {"ApplicationCatalog": common["ApplicationCatalog"], "ApplicationValuesExternal": true},
			"launcher.json": {"ApplicationCatalog": common["ApplicationCatalog"], "SecretSocket": prepared.Layout.Socket, "SecretBrokerUID": prepared.Identity.UID, "SecretPrincipal": "agent", "DeploymentID": prepared.Identity.DeploymentID, "DeploymentOwner": "loki-tools-" + hex.EncodeToString(owner[:8])},
		}
		for name, fields := range changes {
			path := filepath.Join(*overlayDirectory, name)
			var layout map[string]any
			if daemon.ReadJSON(path, &layout) != nil {
				fmt.Fprintln(stderr, "native role layout is unavailable")
				return 1
			}
			for key, value := range fields {
				layout[key] = value
			}
			raw, _ := json.Marshal(layout)
			if safeio.PublishPrivate(path, raw, true) != nil || os.Chown(path, 0, int(*metadataGroup)) != nil || os.Chmod(path, 0640) != nil {
				fmt.Fprintln(stderr, "native application role binding could not be prepared")
				return 1
			}
		}
	}
	if json.NewEncoder(stdout).Encode(map[string]any{"prepared": true, "policy": prepared.Layout.PolicySHA256, "broker_uid": prepared.Identity.UID, "broker_gid": prepared.Identity.GID}) != nil {
		return 1
	}
	return 0
}
