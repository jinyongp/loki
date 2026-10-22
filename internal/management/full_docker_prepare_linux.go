package management

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"loki/internal/tools"
)

type fullDockerRecord struct {
	Schema      int                   `json:"schema"`
	Reservation DeploymentReservation `json:"reservation"`
	Worker      string                `json:"worker"`
	Specs       []fullContainerSpec   `json:"specs"`
	Order       []string              `json:"order"`
}

func (b *DockerFullBackend) deploymentDirectory(r DeploymentReservation) (string, error) {
	if !deploymentIDPattern.MatchString(r.ID) {
		return "", fmt.Errorf("invalid deployment directory identity")
	}
	if err := b.Store.realRoot(); err != nil {
		return "", err
	}
	parent := filepath.Join(b.Store.Root, "full")
	path := filepath.Join(parent, r.ID)
	for _, directory := range []string{parent, path} {
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := realDirectories(directory); err != nil {
			return "", err
		}
	}
	return path, nil
}

func (b *DockerFullBackend) record(r DeploymentReservation) (fullDockerRecord, error) {
	var record fullDockerRecord
	directory, err := b.deploymentDirectory(r)
	if err != nil {
		return record, err
	}
	if err := readOwnedJSON(filepath.Join(directory, "services.json"), tools.MaxManifestBytes, &record); err != nil {
		return record, err
	}
	if record.Schema != 1 || !reflect.DeepEqual(record.Reservation, r) || len(record.Specs) != len(r.Services) || !filepath.IsAbs(record.Worker) {
		return record, fmt.Errorf("deployment service record has incompatible ownership")
	}
	seen := map[string]bool{}
	for _, spec := range record.Specs {
		if !slices.Contains(r.Services, spec.Service) || seen[spec.Service] || spec.Name != b.containerName(r, spec.Service) || len(spec.Command) == 0 || !filepath.IsAbs(spec.Command[0]) || !imageDigestPattern.MatchString(spec.Image) {
			return record, fmt.Errorf("invalid saved deployment service")
		}
		seen[spec.Service] = true
	}
	ordered := map[string]bool{}
	for _, name := range record.Order {
		if !seen[name] || ordered[name] {
			return record, fmt.Errorf("invalid saved startup order")
		}
		ordered[name] = true
	}
	if len(ordered) != len(seen) {
		return record, fmt.Errorf("incomplete saved startup order")
	}
	return record, nil
}

func fullStartupOrder(plan FullPlan) ([]string, error) {
	order := []string{}
	remaining := slices.Clone(plan.Services)
	ready := map[string]bool{}
	for len(remaining) != 0 {
		progress := false
		for i := 0; i < len(remaining); {
			service := remaining[i]
			allowed := true
			for _, dependency := range service.Requires {
				allowed = allowed && ready[dependency]
			}
			if !allowed {
				i++
				continue
			}
			ready[service.Name] = true
			order = append(order, service.Name)
			remaining = slices.Delete(remaining, i, i+1)
			progress = true
		}
		if !progress {
			return nil, fmt.Errorf("full service dependency graph cannot start")
		}
	}
	return order, nil
}

func writeFullFile(path string, data []byte) error {
	if len(data) > tools.MaxManifestBytes {
		return fmt.Errorf("protected deployment file exceeds its bound")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("protected deployment file is not regular")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".prepare-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (b *DockerFullBackend) prepareAuthentication() (string, error) {
	parent := filepath.Join(b.Store.Root, "auth")
	directory := filepath.Join(parent, "mcp")
	for _, path := range []string{parent, directory} {
		if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		if err := realDirectories(path); err != nil {
			return "", err
		}
	}
	path := filepath.Join(directory, "token")
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() != 64 {
			return "", fmt.Errorf("MCP token must be an owned 256-bit regular file")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		if _, err := hex.DecodeString(string(data)); err != nil {
			return "", fmt.Errorf("invalid owned MCP token")
		}
		return directory, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	if err := writeFullFile(path, []byte(hex.EncodeToString(random[:]))); err != nil {
		return "", err
	}
	return directory, nil
}

func (b *DockerFullBackend) volumePath(ctx context.Context, mount FullMount) (string, error) {
	if err := b.ownedVolume(ctx, mount); err != nil {
		return "", err
	}
	data, err := b.command(ctx, "locating an owned data volume", nil, "volume", "inspect", "--format", "{{.Mountpoint}}", mount.Source)
	path := strings.TrimSpace(string(data))
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "\r\n\x00") {
		return "", fmt.Errorf("owned volume has an invalid host source")
	}
	return path, nil
}

func (b *DockerFullBackend) Prepare(ctx context.Context, r DeploymentReservation, t FullTopology, l FullLayouts) error {
	if r.PlanDigest != fullPlanDigest(t.Resources.Plan) || t.Owner != b.Store.FullOwner() {
		return fmt.Errorf("full preparation plan does not match its reservation")
	}
	directory, err := b.deploymentDirectory(r)
	if err != nil {
		return err
	}
	layoutDirectory := filepath.Join(directory, "layout")
	if err := os.Mkdir(layoutDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := realDirectories(layoutDirectory); err != nil {
		return err
	}
	for _, service := range t.Services {
		if service.Name != "secrets" {
			continue
		}
		ancestor := filepath.Join("/dev/shm", t.Owner+"-secret-runtime")
		if err := os.Mkdir(ancestor, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		var owner, st unix.Stat_t
		var fs unix.Statfs_t
		if unix.Lstat(b.Store.Root, &owner) != nil || unix.Lstat(ancestor, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != owner.Uid || unix.Statfs(ancestor, &fs) != nil || fs.Type != unix.TMPFS_MAGIC {
			return fmt.Errorf("application runtime ancestor is not private host-owned tmpfs")
		}
		runtimeRoot := filepath.Join(ancestor, "secrets")
		if err := os.Mkdir(runtimeRoot, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if err := realDirectories(runtimeRoot); err != nil {
			return err
		}
		catalog := json.RawMessage(`{"resources":[]}`)
		if err := readOwnedJSON(filepath.Join(b.Store.Root, "application-resources.json"), tools.MaxManifestBytes, &catalog); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := writeFullFile(filepath.Join(layoutDirectory, "application-resources.json"), catalog); err != nil {
			return err
		}
	}
	authDirectory, err := b.prepareAuthentication()
	if err != nil {
		return err
	}
	images := map[string]bool{}
	for _, service := range t.Services {
		images[service.Image] = true
		for _, mount := range service.Mounts {
			if mount.Kind == "volume" {
				if err := b.ownedVolume(ctx, mount); err != nil {
					return err
				}
			}
		}
		for _, network := range service.Networks {
			if err := b.ownedNetwork(ctx, network, !strings.HasSuffix(network, "-outbound")); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"Image", "GatewayImage"} {
		if image, ok := l.Launcher[key].(string); ok {
			images[image] = true
		}
	}
	for image := range images {
		if err := b.pinnedImage(ctx, image); err != nil {
			return err
		}
	}
	worker, err := t.Resources.ServiceProgram("runtime-core", "loki")
	if err != nil {
		return err
	}
	order, err := fullStartupOrder(t.Resources.Plan)
	if err != nil {
		return err
	}
	record := fullDockerRecord{Schema: 1, Reservation: r, Worker: worker, Specs: []fullContainerSpec{}, Order: order}
	config := []byte("root = \"/workspace\"\nhost = \"127.0.0.1\"\nport = 18765\naudit_log = \"/var/lib/loki/mcp/audit.jsonl\"\n")
	if err := writeFullFile(filepath.Join(layoutDirectory, "loki.toml"), config); err != nil {
		return err
	}
	var githubConfig []byte
	for _, choice := range t.Resources.Plan.Enabled {
		if choice.ID == "github" {
			githubConfig, err = b.Store.ProviderConfiguration("github")
			if err != nil {
				return err
			}
			break
		}
	}
	if len(githubConfig) == 0 {
		githubConfig = []byte("# Configure through loki integrations setup github.\n")
	}
	if err := writeFullFile(filepath.Join(layoutDirectory, "github.toml"), githubConfig); err != nil {
		return err
	}
	if err := writeFullFile(filepath.Join(layoutDirectory, "ingress.toml"), []byte("public_hosts = []\n")); err != nil {
		return err
	}
	var signingPublic *fullSigningPublic
	for _, choice := range t.Resources.Plan.Enabled {
		if choice.ID != "git" || !slices.Contains(choice.Capabilities, "signing") {
			continue
		}
		keygen, err := t.Resources.ServiceProgram("git", "ssh-keygen")
		if err != nil {
			return err
		}
		data, err := b.Store.IntegrationMetadata("git")
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return fmt.Errorf("Git signing needs its identity and key; run loki integrations setup git --identity-name NAME --identity-email EMAIL")
		}
		public, err := decodeFullSigningPublic(data, keygen)
		if err != nil {
			return err
		}
		signingPublic = &public
		for name, value := range map[string]string{"signing.pub": public.Identity.PublicKey + "\n", "signing.gitconfig": public.GitConfig, "allowed-signers": public.AllowedSigners} {
			if err := writeFullFile(filepath.Join(layoutDirectory, name), []byte(value)); err != nil {
				return err
			}
		}
		l.MCP["Environment"].(map[string]string)["GIT_CONFIG_GLOBAL"] = "/etc/loki/layout/signing.gitconfig"
	}
	for i := range t.Services {
		service := &t.Services[i]
		if service.Name == "launcher" {
			l.Launcher["DeploymentOwner"], l.Launcher["DeploymentID"] = t.Owner, r.ID
			if signingPublic != nil {
				l.Launcher["SigningSocketVolume"] = t.Owner + "-socket-signing"
				l.Launcher["SigningPublicKey"] = filepath.Join(layoutDirectory, "signing.pub")
				l.Launcher["SigningGitConfig"] = filepath.Join(layoutDirectory, "signing.gitconfig")
				l.Launcher["SigningAllowedSigners"] = filepath.Join(layoutDirectory, "allowed-signers")
			}
			for _, mount := range slices.Clone(service.Mounts) {
				key := ""
				switch mount.Target {
				case "/workspace":
					key = "Workspace"
				case "/var/lib/loki/launcher":
					key = "StateDirectory"
				case "/var/lib/loki/toolchains":
					key = "ToolchainStore"
				}
				if key == "" {
					continue
				}
				hostPath, err := b.volumePath(ctx, mount)
				if err != nil {
					return err
				}
				l.Launcher[key] = hostPath
				bindings, _ := l.Launcher["Volumes"].([]map[string]string)
				l.Launcher["Volumes"] = append(bindings, map[string]string{"path": hostPath, "name": mount.Source})
				// The launcher validates files through the same owned directory;
				// child containers mount the declared named volume directly.
				service.Mounts = append(service.Mounts, FullMount{Kind: "bind", Source: hostPath, Target: hostPath, ReadOnly: mount.ReadOnly})
			}
		}
		if service.Name == "launcher" || service.Name == "runtime" && l.Runtime["DockerSocket"] == "/run/docker.sock" {
			service.Mounts = append(service.Mounts, FullMount{Kind: "bind", Source: b.Socket, Target: "/run/docker.sock"})
		}
		if service.Name != "secrets" {
			service.Mounts = append(service.Mounts, FullMount{Kind: "bind", Source: layoutDirectory, Target: "/etc/loki/layout", ReadOnly: true})
		}
		if service.Name == "mcp" {
			service.Mounts = append(service.Mounts, FullMount{Kind: "bind", Source: authDirectory, Target: "/etc/loki/auth", ReadOnly: true})
		}
		spec, err := b.serviceSpecification(r, t, *service, l, worker)
		if err != nil {
			return err
		}
		record.Specs = append(record.Specs, spec)
	}
	for name, value := range map[string]map[string]any{"mcp.json": l.MCP, "runtime.json": l.Runtime, "launcher.json": l.Launcher, "executor.json": l.Executor} {
		if value == nil {
			continue
		}
		if err := atomicJSON(filepath.Join(layoutDirectory, name), value); err != nil {
			return err
		}
	}
	bootstrap, err := t.Bootstrap(uint32(os.Geteuid()), layoutDirectory, authDirectory, r.ID)
	if err != nil {
		return err
	}
	if err := b.bootstrap(ctx, r, t, bootstrap); err != nil {
		return err
	}
	for i := range record.Specs {
		spec := &record.Specs[i]
		if spec.Service != "secrets" {
			continue
		}
		var prepared struct {
			Prepared   bool   `json:"prepared"`
			Policy     string `json:"policy"`
			UID        uint32 `json:"broker_uid"`
			GID        uint32 `json:"broker_gid"`
			Deployment string `json:"deployment_id"`
			Runner     string `json:"runner_id"`
			Generation string `json:"active_generation"`
		}
		if err := readOwnedJSON(filepath.Join(layoutDirectory, "secrets-prepared.json"), 4096, &prepared); err != nil {
			return err
		}
		if !prepared.Prepared || prepared.UID != 10006 || prepared.GID != 10006 || !deploymentIDPattern.MatchString(prepared.Deployment) || !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(prepared.Generation) || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(prepared.Policy) || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(prepared.Runner) {
			return fmt.Errorf("owned app generation preparation was not confirmed")
		}
		for j, mount := range spec.Mounts {
			if mount.Kind != "volume" || mount.Target != "/var/lib/loki/secrets" {
				continue
			}
			hostPath, err := b.volumePath(ctx, mount)
			if err != nil {
				return err
			}
			spec.Mounts[j] = FullMount{Kind: "bind", Source: filepath.Join(hostPath, "generations", prepared.Generation), Target: mount.Target}
			spec.Mounts = append(spec.Mounts, FullMount{Kind: "bind", Source: filepath.Join(hostPath, "secrets-admin.json"), Target: "/etc/loki/secret-admin.json", ReadOnly: true})
		}
		spec.Mounts = append(spec.Mounts, FullMount{Kind: "bind", Source: filepath.Join("/dev/shm", t.Owner+"-secret-runtime", "secrets", "layout.json"), Target: "/run/loki/secrets/layout.json", ReadOnly: true})
	}
	if err := atomicJSON(filepath.Join(directory, "services.json"), record); err != nil {
		return err
	}
	for _, spec := range record.Specs {
		if err := b.createContainer(ctx, r, spec); err != nil {
			return err
		}
	}
	return nil
}

func (b *DockerFullBackend) serviceSpecification(r DeploymentReservation, t FullTopology, s FullServiceResources, l FullLayouts, worker string) (fullContainerSpec, error) {
	if s.HostNetwork && s.Name != "endpoints" {
		return fullContainerSpec{}, fmt.Errorf("unexpected host-network role")
	}
	result := fullContainerSpec{Name: b.containerName(r, s.Name), Service: s.Name, Image: s.Image, User: fmt.Sprintf("%d:%d", s.UID, s.GID), Groups: []string{}, Mounts: s.Mounts, Networks: s.Networks, Capabilities: []string{}, Memory: 256 << 20, PIDs: 128}
	result.Tmpfs = map[string]string{"/tmp": fmt.Sprintf("rw,nosuid,nodev,size=134217728,uid=%d,gid=%d,mode=0700", s.UID, s.GID), "/var/tmp": fmt.Sprintf("rw,nosuid,nodev,size=134217728,uid=%d,gid=%d,mode=0700", s.UID, s.GID)}
	for _, group := range s.SupplementaryGroups {
		result.Groups = append(result.Groups, strconv.FormatUint(uint64(group), 10))
	}
	result.HostNetwork = s.HostNetwork
	config := "/etc/loki/layout/loki.toml"
	github := "/etc/loki/layout/github.toml"
	common := []string{"--config", config, "--github-config", github}
	switch s.Name {
	case "secrets":
		program, err := t.Resources.ServiceProgram("secrets", "secret-broker")
		if err != nil {
			return result, err
		}
		result.Command = []string{program, "--layout", "/run/loki/secrets/layout.json"}
		result.Memory, result.PIDs = 512<<20, 128
	case "runtime":
		result.Command = append([]string{worker, "runtime", "--layout", "/etc/loki/layout/runtime.json"}, common...)
		result.Capabilities = []string{"CHOWN", "SETUID", "SETGID", "DAC_OVERRIDE"}
		result.Tmpfs["/var/tmp/loki/runner"] = "rw,nosuid,nodev,size=134217728,uid=10000,gid=10000,mode=0700"
	case "mcp":
		result.Command = append([]string{worker, "mcp", "--layout", "/etc/loki/layout/mcp.json", "--token-file", "/etc/loki/auth/token", "--ingress-config", "/etc/loki/layout/ingress.toml"}, common...)
	case "executor":
		program, err := t.Resources.ServiceProgram("execution", "executor")
		if err != nil {
			return result, err
		}
		result.Command = append([]string{program, "--layout", "/etc/loki/layout/executor.json", "--execution-contract", l.ExecutionContract}, common...)
	case "launcher":
		program, err := t.Resources.ServiceProgram("execution", "launcher")
		if err != nil {
			return result, err
		}
		result.Command = append([]string{program, "--layout", "/etc/loki/layout/launcher.json", "--execution-contract", l.ExecutionContract}, common...)
		for _, setting := range []struct{ flagName, key string }{{"--signing-socket-volume", "SigningSocketVolume"}, {"--signing-public-key-source", "SigningPublicKey"}, {"--signing-git-config-source", "SigningGitConfig"}, {"--signing-allowed-signers-source", "SigningAllowedSigners"}} {
			if value, ok := l.Launcher[setting.key].(string); ok {
				result.Command = append(result.Command, setting.flagName, value)
			}
		}
		result.Capabilities = []string{"CHOWN", "DAC_OVERRIDE"}
	case "egress":
		result.Command = []string{worker, "egress-proxy", "--host", "0.0.0.0", "--port", "18766", "--execution-contract", l.ExecutionContract, "--policy", l.EgressPolicy, "--profile", "dependency-install", "--audit", "/var/log/loki/audit.jsonl"}
	case "browser-proxy":
		result.Command = []string{worker, "browser-proxy", "--host", "0.0.0.0", "--port", "18767", "--execution-contract", l.ExecutionContract, "--port-guard-socket", "/run/loki/runtime/control.sock", "--port-guard-uid", "0"}
		for _, mount := range s.Mounts {
			if mount.Target == "/run/loki/endpoints" {
				result.Command = append(result.Command, "--endpoints-socket", "/run/loki/endpoints/control.sock")
			}
		}
	case "endpoints":
		result.Command = []string{worker, "owned-endpoints", "--socket", "/run/loki/endpoints/control.sock", "--tools-config", "/etc/loki/activation/state.json"}
	case "browser":
		result.Command = []string{worker, "browser", "--socket", "/run/loki/browser/control.sock", "--agent-uid", "10000", "--socket-gid", "10001", "--config", config, "--execution-contract", l.ExecutionContract, "--bundle", "/opt/loki/modules/browser", "--data", "/var/lib/loki/browser", "--workspace", "/var/lib/loki/browser/workspace", "--proxy", "http://browser-proxy:18767"}
		// Chrome needs a writable home for Crashpad even with its own profile.
		// Keep those files inside the browser's owned volume, away from /root.
		result.Command = append([]string{"/usr/bin/env", "HOME=/var/lib/loki/browser", "XDG_CONFIG_HOME=/var/lib/loki/browser/config", "XDG_CACHE_HOME=/var/lib/loki/browser/cache"}, result.Command...)
		for _, choice := range t.Resources.Plan.Enabled {
			if choice.ID == "browser" && len(choice.Capabilities) != 0 {
				result.Command = append(result.Command, "--capabilities", strings.Join(choice.Capabilities, ","))
			}
		}
		// One client owns both upstream engines. Chrome counts renderer threads
		// against this limit too; 256 rejects the second sandboxed engine.
		result.Memory, result.PIDs = 1<<30, 512
		// The reviewed profile lives in the trusted core generation; no
		// unconfined seccomp or disabled Chromium sandbox fallback is used.
		relative, ok := t.Resources.Payloads["runtime-core"].Assets["browser-seccomp"]
		if !ok {
			return result, fmt.Errorf("full browser requires its trusted sandbox seccomp asset")
		}
		profile, err := t.Resources.Plan.Program("runtime-core", relative)
		if err != nil {
			return result, err
		}
		result.Seccomp = profile
	case "git-signing":
		agent, err := t.Resources.ServiceProgram("git", "ssh-agent")
		if err != nil {
			return result, err
		}
		add, err := t.Resources.ServiceProgram("git", "ssh-add")
		if err != nil {
			return result, err
		}
		result.Command = []string{worker, "signing-agent", "--private-socket", "/run/loki/signing/private.sock", "--public-socket", "/run/loki/signing/agent.sock", "--key", "/var/lib/loki/git/signing/key", "--agent-binary", agent, "--add-binary", add, "--tools-config", "/etc/loki/activation/state.json", "--runner-uid", "10000", "--socket-gid", "10001"}
		result.Capabilities = []string{"CHOWN"}
	default:
		return result, fmt.Errorf("unknown owned service %s", s.Name)
	}
	return result, nil
}

func (b *DockerFullBackend) bootstrap(ctx context.Context, r DeploymentReservation, t FullTopology, p FullBootstrap) error {
	image, err := t.Resources.Image("runtime-core", "service")
	if err != nil {
		return err
	}
	args := []string{"container", "create", "--name", b.containerName(r, "prepare"), "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "67108864", "--pids-limit", "32", "--restart", "no", "--entrypoint", "/bin/sh"}
	for _, label := range b.labels(r, "prepare") {
		args = append(args, "--label", label)
	}
	for _, mount := range p.Mounts {
		value, err := dockerMountArgument(mount)
		if err != nil {
			return err
		}
		args = append(args, "--mount", value)
	}
	args = append(args, image, "-ec", p.Script)
	ids, err := b.containerIDs(ctx, r)
	if err != nil {
		return err
	}
	for _, id := range ids {
		current, err := b.inspectContainer(ctx, id)
		if err != nil {
			return err
		}
		if current.Config.Labels[fullServiceLabel] != "prepare" {
			continue
		}
		if err := b.requireParent(r, current); err != nil {
			return err
		}
		if _, err := b.command(ctx, "removing an interrupted owned preparation", nil, "container", "rm", "--force", id); err != nil {
			return err
		}
	}
	if _, err := b.command(ctx, "creating owned data preparation", nil, args...); err != nil {
		return err
	}
	if _, err := b.command(ctx, "preparing selected owner directories", nil, "container", "start", "--attach", b.containerName(r, "prepare")); err != nil {
		return err
	}
	// start --attach alone may return success for a nonzero container exit.
	data, err := b.command(ctx, "checking preparation result", nil, "container", "inspect", "--format", "{{.State.ExitCode}}", b.containerName(r, "prepare"))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != "0" {
		return fmt.Errorf("owned data preparation failed; inspect its bounded diagnostic output")
	}
	_, err = b.command(ctx, "removing completed data preparation", nil, "container", "rm", b.containerName(r, "prepare"))
	return err
}
