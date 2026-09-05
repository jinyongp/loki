// Package action owns the private launch boundary between runtime and sandbox.
package action

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const maxPayloadBytes = 2 * 1048576
const requiredSeals = unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE

type payload struct {
	Version                         int      `json:"version"`
	Argv                            []string `json:"argv"`
	Environment                     []string `json:"environment"`
	CWD                             string   `json:"cwd"`
	HostWorkspace                   string   `json:"host_workspace,omitempty"`
	UID, GID                        uint32
	ParentMountNS, ParentPIDNS      uint64
	ParentUserNS                    uint64
	WorkspaceDevice, WorkspaceInode uint64
	Materialized                    *materializedMountPayload `json:"materialized,omitempty"`
}

func (p payload) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "[private action payload]")
}
func (p payload) MarshalJSON() ([]byte, error) {
	return nil, errors.New("private action payload cannot be serialized")
}

type payloadWire payload

func (p payload) validate() error {
	if p.HostWorkspace != "" {
		if err := validDockerHostRoot(p.HostWorkspace); err != nil {
			return err
		}
	}
	if p.Version != 1 || p.UID == 0 || p.ParentMountNS == 0 || p.ParentPIDNS == 0 || p.WorkspaceInode == 0 || len(p.Argv) == 0 || len(p.Argv) > 160 || len(p.Environment) > 600 {
		return errors.New("invalid action payload")
	}
	if !strings.HasPrefix(p.Argv[0], "/") || path.Clean(p.Argv[0]) != p.Argv[0] {
		return errors.New("action executable must be resolved before launch")
	}
	if p.CWD != "/workspace" && !strings.HasPrefix(p.CWD, "/workspace/") || path.Clean(p.CWD) != p.CWD || strings.ContainsAny(p.CWD, "\x00\\") {
		return errors.New("invalid action workspace cwd")
	}
	for _, arg := range p.Argv {
		if len(arg) > 131071 || strings.ContainsRune(arg, 0) || !utf8.ValidString(arg) {
			return errors.New("invalid action argument")
		}
	}
	seen := map[string]bool{}
	for _, entry := range p.Environment {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" || seen[key] || strings.ContainsRune(entry, 0) || !utf8.ValidString(entry) || len(entry) > 131071 {
			return errors.New("invalid action environment")
		}
		seen[key] = true
	}
	if m := p.Materialized; m != nil {
		if m.TargetInode == 0 || p.ParentUserNS == 0 || !strings.HasPrefix(m.Target, p.CWD+"/") || !strings.HasPrefix(m.Target, "/workspace/") || path.Clean(m.Target) != m.Target || strings.ContainsAny(m.Target, "\x00\\") || len(m.Data) > maxPayloadBytes {
			return errors.New("invalid materialization payload")
		}
	}
	return nil
}

func (p payload) seal() (*os.File, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payloadWire(p))
	if err != nil || len(encoded) > maxPayloadBytes {
		return nil, errors.New("action payload exceeds limit")
	}
	defer clear(encoded)
	return sealedBytes(encoded)
}

func sealedBytes(encoded []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("loki-action", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "private action payload")
	ok := false
	defer func() {
		if !ok {
			file.Close()
		}
	}()
	if err = file.Chmod(0600); err != nil {
		return nil, err
	}
	if _, err = file.Write(encoded); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, requiredSeals); err != nil {
		return nil, err
	}
	ok = true
	return file, nil
}

func readPayload(file *os.File) (payload, error) {
	var result payload
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&requiredSeals != requiredSeals {
		return result, errors.New("action input must be a sealed payload")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPayloadBytes {
		return result, errors.New("invalid action payload file")
	}
	encoded, err := io.ReadAll(io.LimitReader(file, maxPayloadBytes+1))
	if err != nil || len(encoded) > maxPayloadBytes || !utf8.Valid(encoded) {
		return result, errors.New("invalid action payload encoding")
	}
	defer clear(encoded)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode((*payloadWire)(&result)); err != nil {
		return payload{}, errors.New("invalid action payload")
	}
	var trailing any
	if err = decoder.Decode(&trailing); err != io.EOF {
		return payload{}, errors.New("invalid action payload")
	}
	return result, result.validate()
}

func namespaceInode(name string) (uint64, error) {
	var info unix.Stat_t
	err := unix.Stat("/proc/self/ns/"+name, &info)
	return info.Ino, err
}

func (p payload) verifySandbox() error {
	return p.verifySandboxCaps(false)
}

func (p payload) verifySandboxCaps(mounting bool) error {
	if os.Getuid() != int(p.UID) || os.Geteuid() != int(p.UID) || os.Getgid() != int(p.GID) {
		return errors.New("action identity mismatch")
	}
	mount, err := namespaceInode("mnt")
	if err != nil {
		return err
	}
	pid, err := namespaceInode("pid")
	if err != nil {
		return err
	}
	if mount == p.ParentMountNS || pid == p.ParentPIDNS {
		return errors.New("action sandbox namespace is missing")
	}
	if mounting {
		user, err := namespaceInode("user")
		if err != nil || p.ParentUserNS == 0 || user == p.ParentUserNS {
			return errors.New("materialization requires a new user namespace")
		}
	}
	var workspace unix.Stat_t
	if err = unix.Stat("/workspace", &workspace); err != nil {
		return err
	}
	if uint64(workspace.Dev) != p.WorkspaceDevice || workspace.Ino != p.WorkspaceInode {
		return errors.New("action workspace identity mismatch")
	}
	var header unix.CapUserHeader
	var caps [2]unix.CapUserData
	header.Version = unix.LINUX_CAPABILITY_VERSION_3
	if err = unix.Capget(&header, &caps[0]); err != nil {
		return err
	}
	for index, cap := range caps {
		var allowed uint32
		if mounting && index == 0 {
			allowed = 1 << unix.CAP_SYS_ADMIN
			if cap.Effective != allowed || cap.Permitted != allowed {
				return errors.New("materialization mount capability is missing")
			}
		}
		if cap.Effective & ^allowed != 0 || cap.Permitted & ^allowed != 0 || cap.Inheritable & ^allowed != 0 {
			return errors.New("action capabilities were not dropped")
		}
	}
	return nil
}

// ExecInSandbox is a private binary entrypoint. It never adds credentials to
// the environment of the controller, runuser, bubblewrap, or this Go helper.
// Credentials become an environment only at the final exec, after all guards.
func ExecInSandbox(stdin *os.File) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p, err := readPayload(stdin)
	if err != nil {
		return err
	}
	if err = p.verifySandboxCaps(p.Materialized != nil); err != nil {
		return executionStageError{"sandbox-guards", err}
	}
	if p.HostWorkspace != "" {
		var stat unix.Stat_t
		if err := unix.Stat(p.HostWorkspace, &stat); err != nil {
			return err
		}
		if stat.Ino != p.WorkspaceInode || uint64(stat.Dev) != p.WorkspaceDevice {
			return errors.New("Docker host workspace alias mismatch")
		}
	}
	if err = p.injectMaterializedMount(); err != nil {
		var stage executionStageError
		if errors.As(err, &stage) {
			return err
		}
		return executionStageError{"materialization-guards", err}
	}
	if p.Materialized != nil {
		var caps [2]unix.CapUserData
		header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
		if err := unix.Capset(&header, &caps[0]); err != nil {
			return err
		}
		if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
			return err
		}
		if err := p.verifySandbox(); err != nil {
			return err
		}
	}
	if err = unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	cwd := p.CWD
	if p.HostWorkspace != "" {
		cwd = path.Join(p.HostWorkspace, strings.TrimPrefix(p.CWD, "/workspace"))
	}
	if err = os.Chdir(cwd); err != nil {
		return err
	}
	input, err := os.Open("/dev/null")
	if err != nil {
		return err
	}
	if input.Fd() != 0 {
		if err = unix.Dup3(int(input.Fd()), 0, 0); err != nil {
			input.Close()
			return err
		}
		input.Close()
	}
	if err = unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return err
	}
	return syscall.Exec(p.Argv[0], p.Argv, p.Environment)
}
