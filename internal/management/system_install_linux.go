package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"loki/internal/tools"
)

type systemServiceRecord struct {
	Schema   int               `json:"schema"`
	UID      uint32            `json:"uid"`
	Units    map[string]string `json:"units"`
	Previous map[string]string `json:"previous"`
	Removing bool              `json:"removing,omitempty"`
}

func SystemSelection(uid uint32) ExecutionSelection {
	id := strconv.FormatUint(uint64(uid), 10)
	return ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "local"}, System: true, Owned: true, Root: "/var/lib/loki-tools/" + id, Command: "/usr/local/lib/loki-tools/" + id + "/bin/loki", Socket: "/run/loki-tools-manager-" + id + ".sock"}
}

// InstallSystemHost is invoked through explicit administrator authentication.
// The service and its child commands retain a root-owned manager, state and
// socket. Only the chosen login UID is admitted to the finite CLI protocol.
func InstallSystemHost(ctx context.Context, uid uint32, source string, diagnostics io.Writer) (ExecutionSelection, error) {
	selected := SystemSelection(uid)
	if os.Geteuid() != 0 || uid == 0 {
		return selected, fmt.Errorf("system host preparation requires administrator authorization for a non-root login user")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return selected, fmt.Errorf("persistent Linux host preparation requires systemd; select a managed WSL or prepared SSH host")
	}
	account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return selected, err
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return selected, err
	}
	store := Store{Root: selected.Root}
	for _, path := range []string{store.Root, filepath.Dir(selected.Command), "/etc/systemd/system"} {
		if err := requireAdministratorPath(path); err != nil {
			return selected, err
		}
	}
	if _, err := store.InstallManager(ctx, source, filepath.Dir(selected.Command)); err != nil {
		return selected, err
	}
	if err := store.ConfigureMode(tools.Full); err != nil {
		return selected, err
	}
	if err := PrepareEnvironment(ctx, nil, diagnostics); err != nil {
		return selected, err
	}
	service := fmt.Sprintf("loki-tools-manager-%d.service", uid)
	socket := fmt.Sprintf("loki-tools-manager-%d.socket", uid)
	units := map[string]string{
		service: fmt.Sprintf("[Unit]\nDescription=Loki selected tools management for UID %d\nRequires=%s\nAfter=docker.service\n\n[Service]\nExecStart=%s --host local --root %s _system-host --socket %s --owner-uid %d\nUser=root\nGroup=root\nEnvironment=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nNoNewPrivileges=yes\nPrivateTmp=yes\nRestart=on-failure\nRestartSec=2\n", uid, socket, selected.Command, selected.Root, selected.Socket, uid),
		socket:  fmt.Sprintf("[Unit]\nDescription=Loki selected tools socket for UID %d\n\n[Socket]\nListenStream=%s\nSocketUser=root\nSocketGroup=%d\nSocketMode=0660\nRemoveOnStop=yes\n\n[Install]\nWantedBy=sockets.target\n", uid, selected.Socket, gid),
	}
	recordPath := filepath.Join(store.Root, "system-host.json")
	var previous systemServiceRecord
	if err := readOwnedJSON(recordPath, tools.MaxManifestBytes, &previous); err != nil && !os.IsNotExist(err) {
		return selected, err
	}
	if previous.Schema != 0 && (previous.Schema != 1 || previous.UID != uid || len(previous.Units) != 2) {
		return selected, fmt.Errorf("system service ownership is invalid")
	}
	if previous.Removing {
		return selected, fmt.Errorf("system host removal is interrupted; retry loki hosts remove --purge")
	}
	record := systemServiceRecord{Schema: 1, UID: uid, Units: map[string]string{}, Previous: map[string]string{}}
	for name, body := range units {
		wanted := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
		record.Units[name] = wanted
		path := filepath.Join("/etc/systemd/system", name)
		info, err := os.Lstat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return selected, fmt.Errorf("system unit is not a regular owned file")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return selected, err
			}
			actual := fmt.Sprintf("%x", sha256.Sum256(data))
			if previous.Schema == 0 || (actual != previous.Units[name] && actual != previous.Previous[name]) {
				return selected, fmt.Errorf("existing system unit has foreign ownership; preserving it")
			}
			record.Previous[name] = actual
		} else if !os.IsNotExist(err) {
			return selected, err
		}
	}
	if err := atomicJSON(recordPath, record); err != nil {
		return selected, err
	}
	for name, body := range units {
		file, err := os.CreateTemp("/etc/systemd/system", ".loki-tools-")
		if err != nil {
			return selected, err
		}
		path := file.Name()
		defer os.Remove(path)
		if _, err := io.WriteString(file, body); err != nil {
			file.Close()
			return selected, err
		}
		if err := file.Chmod(0644); err != nil {
			file.Close()
			return selected, err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return selected, err
		}
		if err := file.Close(); err != nil {
			return selected, err
		}
		if err := os.Rename(path, filepath.Join("/etc/systemd/system", name)); err != nil {
			return selected, err
		}
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", socket}, {"restart", service}} {
		command := exec.CommandContext(ctx, "systemctl", args...)
		command.Stdout, command.Stderr = diagnostics, diagnostics
		if err := command.Run(); err != nil {
			return selected, fmt.Errorf("system host preparation interrupted; retry loki setup: %w", err)
		}
	}
	return selected, nil
}

func PrepareSystemHost(ctx context.Context, frontend Store, input io.Reader, diagnostics io.Writer) (*ExecutionSelection, error) {
	return prepareSystemHost(ctx, frontend, input, diagnostics, false)
}

func PrepareSystemHostPassword(ctx context.Context, frontend Store, password []byte, diagnostics io.Writer) (*ExecutionSelection, error) {
	if len(password) > 4096 || bytes.ContainsAny(password, "\x00\r\n") {
		return nil, fmt.Errorf("invalid bounded administrator input")
	}
	material := append(bytes.Clone(password), '\n')
	defer clear(material)
	return prepareSystemHost(ctx, frontend, bytes.NewReader(material), diagnostics, true)
}

func prepareSystemHost(ctx context.Context, frontend Store, input io.Reader, diagnostics io.Writer, password bool) (*ExecutionSelection, error) {
	if os.Geteuid() == 0 {
		return nil, nil
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(diagnostics, "Preparing the persistent Linux tools host. Administrator authentication is required once.")
	args := []string{binary, "_prepare-system-host", "--owner-uid", strconv.Itoa(os.Getuid())}
	if password {
		args = append([]string{"-S", "-p", ""}, args...)
	}
	command := exec.CommandContext(ctx, "sudo", args...)
	command.Stdin, command.Stderr = input, diagnostics
	var response boundedDockerOutput
	response.limit = 1 << 20
	command.Stdout = &response
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("system host preparation failed; retry loki setup: %w", err)
	}
	var selected ExecutionSelection
	if response.overflow || json.Unmarshal(response.Bytes(), &selected) != nil || selected.Validate() != nil {
		return nil, fmt.Errorf("system host returned invalid preparation metadata")
	}
	expected := SystemSelection(uint32(os.Getuid()))
	if selected != expected {
		return nil, fmt.Errorf("system host identity differs from the login user")
	}
	if err := frontend.SelectExecutionHost(selected); err != nil {
		return nil, err
	}
	return &selected, nil
}

func RemoveSystemHost(ctx context.Context, uid uint32, diagnostics io.Writer) error {
	if os.Geteuid() != 0 || uid == 0 {
		return fmt.Errorf("owned system host removal requires administrator authorization")
	}
	selected := SystemSelection(uid)
	store := Store{Root: selected.Root, hostRemoval: true}
	for _, path := range []string{store.Root, filepath.Dir(selected.Command), "/etc/systemd/system"} {
		if err := requireAdministratorPath(path); err != nil {
			return err
		}
	}
	// Detaching frontend state can fail after the administrator removal already
	// succeeded. A retry is harmless only when every fixed owned resource is absent.
	absent := true
	for _, path := range []string{store.Root, selected.Command, selected.Command + ".loki-owner.json", selected.Socket,
		fmt.Sprintf("/etc/systemd/system/loki-tools-manager-%d.service", uid), fmt.Sprintf("/etc/systemd/system/loki-tools-manager-%d.socket", uid)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			absent = false
		}
	}
	if absent {
		return nil
	}
	var record systemServiceRecord
	if err := readOwnedJSON(filepath.Join(store.Root, "system-host.json"), tools.MaxManifestBytes, &record); err != nil {
		return err
	}
	if record.Schema != 1 || record.UID != uid || len(record.Units) != 2 {
		return fmt.Errorf("system host ownership is invalid")
	}
	if _, err := store.ManagerExecutable(selected.Command); err != nil {
		if !record.Removing || !os.IsNotExist(err) {
			return err
		}
	}
	names := []string{fmt.Sprintf("loki-tools-manager-%d.socket", uid), fmt.Sprintf("loki-tools-manager-%d.service", uid)}
	for _, name := range names {
		path := filepath.Join("/etc/systemd/system", name)
		info, err := os.Lstat(path)
		if record.Removing && os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("system unit ownership differs; preserving host")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(raw))
		if digest != record.Units[name] && digest != record.Previous[name] {
			return fmt.Errorf("system unit ownership differs; preserving host")
		}
	}
	backend, err := NewFullBackend(store, diagnostics)
	if err != nil {
		return err
	}
	owned, ok := backend.(*DockerFullBackend)
	if !ok {
		return fmt.Errorf("system host requires its local owned Docker backend")
	}
	if err := owned.RequireIdle(ctx); err != nil {
		return err
	}
	record.Removing = true
	if err := atomicJSON(filepath.Join(store.Root, "system-host.json"), record); err != nil {
		return err
	}
	if err := store.StopFull(ctx, backend); err != nil {
		return err
	}
	if err := owned.PurgeOwnedData(ctx); err != nil {
		return err
	}
	if err := store.UninstallTools(ctx); err != nil {
		return err
	}
	for _, args := range [][]string{{"disable", "--now", names[0]}, {"stop", names[1]}} {
		if _, err := os.Lstat(filepath.Join("/etc/systemd/system", args[len(args)-1])); os.IsNotExist(err) {
			continue
		}
		command := exec.CommandContext(ctx, "systemctl", args...)
		command.Stdout, command.Stderr = diagnostics, diagnostics
		if err := command.Run(); err != nil {
			return err
		}
	}
	for _, name := range names {
		if err := os.Remove(filepath.Join("/etc/systemd/system", name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	command := exec.CommandContext(ctx, "systemctl", "daemon-reload")
	command.Stdout, command.Stderr = diagnostics, diagnostics
	if err := command.Run(); err != nil {
		return err
	}
	if err := realDirectories(store.Root, filepath.Dir(selected.Command)); err != nil {
		return err
	}
	if err := os.Remove(selected.Command); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(selected.Command + ".loki-owner.json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Keep unknown files in the shared command directory. The private data root
	// is proved by its root-owned per-UID service and manager ownership records.
	return os.RemoveAll(store.Root)
}

func (s Store) requireSystemMutable() error {
	if s.hostRemoval {
		return nil
	}
	var record systemServiceRecord
	if err := readOwnedJSON(filepath.Join(s.Root, "system-host.json"), tools.MaxManifestBytes, &record); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if record.Schema != 1 || record.UID == 0 || len(record.Units) != 2 {
		return fmt.Errorf("invalid system host ownership")
	}
	if record.Removing {
		return fmt.Errorf("system host removal is interrupted; retry loki hosts remove --purge")
	}
	return nil
}

func requireAdministratorPath(path string) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !info.IsDir() || !ok || stat.Uid != 0 || info.Mode().Perm()&0022 != 0 {
				return fmt.Errorf("managed administrator path is not root-owned and protected: %s", current)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if current == "/" {
			return nil
		}
	}
}
