package action

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/process"
	"loki/internal/redact"
	"loki/internal/secret"
)

const sandboxBinary = "/loki-action-exec"

type PublicMount struct{ Source, Target string }

// Layout is administrator-owned service configuration, never an MCP request.
// PublicMounts permits only existing public toolchain/config/signing surfaces.
type Layout struct {
	RuntimeSocket                    string
	RuntimeUID                       uint32
	Workspace, Binary, Bwrap, Runner string
	UID, GID                         uint32
	SystemdScope                     bool
	MaxProfileProcesses              int
	PublicMounts                     []PublicMount
	PreviewBaseDomain                string
	MaterializationDirectory         string
	MaterializationRecoveryDirectory string
	SnapshotDirectory                string
}

type Launch struct {
	mu              sync.Mutex
	started         bool
	spec            process.StartSpec
	files           []*os.File
	materialization *materialization
}

func (l *Launch) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, file := range l.files {
		file.Close()
	}
	l.files = nil
	if l.materialization != nil {
		_ = l.materialization.close()
		l.materialization = nil
	}
}
func (l *Launch) Start(manager *process.Manager) (map[string]any, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started || l.files == nil {
		return nil, errors.New("action launch was already consumed or closed")
	}
	l.started = true
	result, err := manager.Start(l.spec)
	if err == nil && result["reused"] != true {
		l.materialization = nil
	} // Manager now owns cleanup.
	if public, ok := err.(fault.Error); ok {
		return nil, fault.Error(string(public) + "; stop an existing action process before retrying")
	}
	return result, err
}

func pinned(path string, directory bool) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("action mount source must be absolute")
	}
	flags := unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "pinned action mount")
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !(info.IsDir() || info.Mode().IsRegular()) || directory && !info.IsDir() {
		file.Close()
		return nil, errors.New("unsafe action mount source")
	}
	return file, nil
}

func actionEnvironment(plan secret.ActionPlan, parameters launchParameters) []string {
	env := map[string]string{
		"HOME": "/home/runner", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "PATH": "/home/linuxbrew/.linuxbrew/bin:/usr/local/bin:/usr/bin:/bin", "CI": "1", "TMPDIR": "/tmp",
		"COREPACK_HOME": "/workspace/.loki/corepack", "PNPM_HOME": "/workspace/.loki/corepack", "GIT_CONFIG_GLOBAL": "/etc/loki/gitconfig", "GIT_CONFIG_NOSYSTEM": "1", "SSH_AUTH_SOCK": "/run/loki/signing/agent.sock",
		"GOMODCACHE": "/workspace/.loki/go/pkg/mod", "GOPATH": "/workspace/.loki/go", "GOPROXY": "https://proxy.golang.org",
		"CARGO_HOME": "/workspace/.loki/cargo", "RUSTUP_HOME": "/workspace/.loki/rustup", "CARGO_REGISTRIES_CRATES_IO_PROTOCOL": "sparse", "CARGO_NET_GIT_FETCH_WITH_CLI": "true",
		"NPM_CONFIG_REGISTRY": "https://registry.npmjs.org", "npm_config_store_dir": "/workspace/.loki/pnpm-store", "HTTP_PROXY": "http://127.0.0.1:8766", "HTTPS_PROXY": "http://127.0.0.1:8766", "http_proxy": "http://127.0.0.1:8766", "https_proxy": "http://127.0.0.1:8766", "NO_PROXY": "127.0.0.1,localhost", "no_proxy": "127.0.0.1,localhost", "NODE_USE_ENV_PROXY": "1",
	}
	for _, entry := range plan.SecretEnvironment() {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}
	if dynamic := plan.Policy.DynamicPort; dynamic != nil {
		env[dynamic.Environment] = strconv.Itoa(parameters.port)
		if dynamic.OriginEnvironment != "" {
			env[dynamic.OriginEnvironment] = localURL(parameters.port)
		}
	}
	for key, value := range parameters.publicEnvironment {
		env[key] = value
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

// Prepare pins all mount sources and seals a private input before starting any
// process. The returned launch owns its descriptors until Start has returned.
func Prepare(layout Layout, plan secret.ActionPlan) (*Launch, error) {
	return prepare(layout, plan, launchParameters{})
}

type launchParameters struct {
	port              int
	publicEnvironment map[string]string
}

func instanceKey(plan secret.ActionPlan) *string {
	if !plan.Policy.Singleton {
		return nil
	}
	digest := sha256.Sum256([]byte(plan.Profile + "\x00" + plan.Action + "\x00" + plan.CWD))
	key := hex.EncodeToString(digest[:])
	return &key
}

func prepare(layout Layout, plan secret.ActionPlan, parameters launchParameters) (*Launch, error) {
	if layout.UID == 0 || !filepath.IsAbs(layout.Bwrap) || !filepath.IsAbs(layout.Workspace) || !filepath.IsAbs(layout.Binary) {
		return nil, errors.New("invalid action sandbox layout")
	}
	if os.Geteuid() == 0 && (layout.Runner == "" || !layout.SystemdScope) {
		return nil, errors.New("privileged action launch requires a runner and service scope")
	}
	if os.Geteuid() != 0 && (os.Geteuid() != int(layout.UID) || os.Getgid() != int(layout.GID)) {
		return nil, errors.New("action runner identity is unavailable")
	}
	if os.Geteuid() == 0 {
		runner, err := user.Lookup(layout.Runner)
		if err != nil || runner.Uid != strconv.FormatUint(uint64(layout.UID), 10) || runner.Gid != strconv.FormatUint(uint64(layout.GID), 10) {
			return nil, errors.New("action runner does not match the configured identity")
		}
	}
	if layout.MaxProfileProcesses == 0 {
		layout.MaxProfileProcesses = 6
	}
	if layout.MaxProfileProcesses < 1 || layout.MaxProfileProcesses > 32 {
		return nil, errors.New("invalid action profile process limit")
	}
	relative, err := filepath.Rel(layout.Workspace, plan.CWD)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") || filepath.ToSlash(filepath.Join("/workspace", relative)) != plan.VisibleCWD {
		return nil, errors.New("action plan does not match sandbox workspace")
	}
	if plan.Policy.DockerAccess {
		return nil, fault.Error("action requires launch features that are not yet connected")
	}
	if plan.Policy.DynamicPort != nil && (parameters.port < 1 || parameters.port > 65535) {
		return nil, errors.New("dynamic action requires an allocated port")
	}
	launch := &Launch{}
	ok := false
	defer func() {
		if !ok {
			launch.Close()
		}
	}()
	workspace, err := pinned(layout.Workspace, true)
	if err != nil {
		return nil, err
	}
	launch.files = append(launch.files, workspace)
	command := slices.Clone(plan.Policy.Command)
	if plan.Policy.DynamicPort != nil {
		for i, item := range command {
			if item == "{LOKI_PORT}" {
				command[i] = strconv.Itoa(parameters.port)
			}
		}
	}
	command, err = resolvedCommand(command, workspace, relative)
	if err != nil {
		return nil, err
	}
	binary, err := pinned(layout.Binary, false)
	if err != nil {
		return nil, err
	}
	launch.files = append(launch.files, binary)
	info, err := binary.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil, errors.New("action helper is not executable")
	}
	if plan.Policy.LockProbe != "" {
		if !relativeFile(plan.Policy.LockProbe) {
			return nil, fault.Error("action lock probe must stay inside action cwd")
		}
		target := filepath.ToSlash(filepath.Join(relative, plan.Policy.LockProbe))
		result, err := runnerFileOperation(context.Background(), layout, workspace, binary, nil, "probe", target)
		if err != nil {
			return nil, err
		}
		if result.Held {
			return nil, fault.Error("action runtime is already owned outside the current Loki session: /workspace/" + target + "; stop the owner or run this action from another worktree")
		}
	}
	environment := actionEnvironment(plan, parameters)
	var snapshots *os.File
	var materialized *materializedMountPayload
	if plan.Policy.MaterializeEnvFile != "" {
		if !filepath.IsAbs(layout.SnapshotDirectory) {
			return nil, errors.New("action snapshot directory is not configured")
		}
		snapshots, err = pinned(layout.SnapshotDirectory, true)
		if err != nil {
			return nil, err
		}
		launch.files = append(launch.files, snapshots)
		launch.materialization, err = beginMaterialization(layout, plan, workspace, binary)
		if err != nil {
			return nil, err
		}
		encoded := materializedEnvironment(plan, environment)
		defer clear(encoded)
		record, err := readMaterializationRecord(launch.materialization.marker)
		if err != nil || record == nil {
			return nil, errors.New("materialization record is unavailable")
		}
		materialized = &materializedMountPayload{Target: "/workspace/" + launch.materialization.target, TargetDevice: record.Device, TargetInode: record.Inode, Data: encoded}
		for _, replacement := range []string{plan.Policy.MaterializeEnvFile + "=/workspace/" + launch.materialization.target, "TMPDIR=" + sandboxSnapshots} {
			key, _, _ := strings.Cut(replacement, "=")
			environment = slices.DeleteFunc(environment, func(entry string) bool { return strings.HasPrefix(entry, key+"=") })
			environment = append(environment, replacement)
		}
	}
	var stat unix.Stat_t
	if err = unix.Fstat(int(workspace.Fd()), &stat); err != nil {
		return nil, err
	}
	mountNS, err := namespaceInode("mnt")
	if err != nil {
		return nil, err
	}
	pidNS, err := namespaceInode("pid")
	if err != nil {
		return nil, err
	}
	userNS, err := namespaceInode("user")
	if err != nil {
		return nil, err
	}
	data := payload{Version: 1, Argv: command, Environment: environment, CWD: plan.VisibleCWD,
		UID: layout.UID, GID: layout.GID, ParentMountNS: mountNS, ParentPIDNS: pidNS, ParentUserNS: userNS, WorkspaceDevice: uint64(stat.Dev), WorkspaceInode: stat.Ino, Materialized: materialized}
	extraFiles := []*os.File{workspace, binary}
	argv := []string{layout.Bwrap, "--die-with-parent", "--new-session", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try", "--uid", strconv.FormatUint(uint64(layout.UID), 10), "--gid", strconv.FormatUint(uint64(layout.GID), 10), "--cap-drop", "ALL",
		"--ro-bind", "/usr", "/usr", "--symlink", "usr/bin", "/bin", "--symlink", "usr/sbin", "/sbin", "--symlink", "usr/lib", "/lib", "--symlink", "usr/lib64", "/lib64", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		"--dir", "/home/runner/.ssh", "--dir", "/etc/loki", "--dir", "/run/loki", "--ro-bind", "/etc/passwd", "/etc/passwd", "--ro-bind", "/etc/group", "/etc/group", "--ro-bind", "/etc/hosts", "/etc/hosts", "--ro-bind", "/etc/ssl/certs", "/etc/ssl/certs", "--ro-bind", "/etc/alternatives", "/etc/alternatives",
		"--bind-fd", "3", "/workspace", "--ro-bind-fd", "4", sandboxBinary}
	for _, source := range layout.PublicMounts {
		if !slices.Contains([]string{"/home/linuxbrew/.linuxbrew", "/home/runner/.local/share/fnm", "/etc/loki/gitconfig", "/home/runner/.ssh/id_ed25519.pub", "/run/loki/signing"}, source.Target) {
			return nil, errors.New("action mount target is not allowlisted")
		}
		file, err := pinned(source.Source, false)
		if err != nil {
			return nil, err
		}
		launch.files = append(launch.files, file)
		extraFiles = append(extraFiles, file)
		argv = append(argv, "--ro-bind-fd", strconv.Itoa(2+len(extraFiles)), source.Target)
	}
	if launch.materialization != nil {
		// Only the trusted helper receives this capability in the new user
		// namespace. It drops all active/inheritable/ambient caps and sets
		// no-new-privileges before exec of the registered command.
		argv = append(argv, "--cap-add", "CAP_SYS_ADMIN")
		extraFiles = append(extraFiles, snapshots)
		argv = append(argv, "--bind-fd", strconv.Itoa(2+len(extraFiles)), sandboxSnapshots)
	}
	input, err := data.seal()
	if err != nil {
		return nil, err
	}
	launch.files = append(launch.files, input)
	argv = append(argv, "--chdir", "/workspace", "--", sandboxBinary, "internal", "action-exec")
	if os.Geteuid() == 0 {
		argv = append([]string{"/usr/sbin/runuser", "-u", layout.Runner, "--"}, argv...)
	}
	if layout.SystemdScope {
		var suffix [8]byte
		if _, err = rand.Read(suffix[:]); err != nil {
			return nil, err
		}
		argv = append([]string{"/usr/bin/systemd-run", "--scope", "--quiet", "--collect", "--unit=loki-action-" + hex.EncodeToString(suffix[:]), "--property=MemoryMax=2G", "--"}, argv...)
	}
	redactionValues := plan.RedactionValues()
	if plan.Policy.MaterializeEnvFile != "" {
		for _, value := range plan.RedactionValues() {
			if encoded := dotenvValue(value); encoded != value {
				redactionValues = append(redactionValues, encoded)
			}
		}
	}
	filter, err := redact.New(redactionValues)
	if err != nil {
		return nil, err
	}
	group := plan.Profile
	launch.spec = process.StartSpec{Spec: process.Spec{Argv: argv, CWD: "/", Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}, Timeout: time.Duration(plan.Policy.TimeoutSeconds) * time.Second, MaxOutput: plan.Policy.MaxOutputBytes},
		Name: plan.Profile + "/" + plan.Action, Group: &group, MaxGroupProcesses: &layout.MaxProfileProcesses, Redactor: filter, Stdin: input, ExtraFiles: extraFiles,
		Metadata: map[string]any{"profile": plan.Profile, "action": plan.Action, "cwd": plan.VisibleCWD}}
	launch.spec.InstanceKey = instanceKey(plan)
	if launch.materialization != nil {
		launch.spec.Cleanup = launch.materialization.close
	}
	if plan.Policy.DynamicPort != nil {
		launch.spec.Metadata["port"] = parameters.port
		launch.spec.Metadata["local_url"] = localURL(parameters.port)
	}
	ok = true
	return launch, nil
}

func (l *Launch) Format(state fmt.State, _ rune) { _, _ = fmt.Fprint(state, "[private action launch]") }
func (l *Launch) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private action launch cannot be serialized")
}
