// Package portguard confines listener inspection and termination to workspace processes.
package portguard

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Guard struct {
	Root string
	UID  uint32
	proc string
}
type listener struct {
	PID          int
	Started      uint64
	CWD, Command string
}

var scopePattern = regexp.MustCompile(`^/system\.slice/loki-(?:action|project-bootstrap)-[0-9a-f]{16}\.scope$`)

func Validate(port int) error {
	if port < 1024 || port > 65535 {
		return errors.New("port must be an integer between 1024 and 65535")
	}
	if port == 8765 || port == 8766 || port == 8767 {
		return errors.New("protected Loki service port")
	}
	return nil
}
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
func (g *Guard) procRoot() string {
	if g.proc != "" {
		return g.proc
	}
	return "/proc"
}
func (g *Guard) managed(pid int) bool {
	data, err := os.ReadFile(filepath.Join(g.procRoot(), strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		path := strings.TrimRight(parts[2], "/")
		if strings.HasSuffix(path, "/loki-mcp.service") || strings.HasSuffix(path, "/loki-runtime.service") || scopePattern.MatchString(path) {
			return true
		}
	}
	return false
}
func (g *Guard) identity(pid int) (listener, uint32, error) {
	base := filepath.Join(g.procRoot(), strconv.Itoa(pid))
	result := listener{PID: pid}
	status, err := os.ReadFile(filepath.Join(base, "status"))
	if err != nil {
		return result, 0, err
	}
	var uid uint64
	found := false
	for _, line := range strings.Split(string(status), "\n") {
		if strings.HasPrefix(line, "Uid:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				break
			}
			uid, err = strconv.ParseUint(fields[1], 10, 32)
			if err != nil {
				return result, 0, err
			}
			found = true
			break
		}
	}
	if !found {
		return result, 0, errors.New("unable to determine listener owner")
	}
	stat, err := os.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return result, 0, err
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return result, 0, errors.New("invalid process identity")
	}
	fields := strings.Fields(string(stat)[end+1:])
	if len(fields) <= 19 {
		return result, 0, errors.New("invalid process identity")
	}
	result.Started, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return result, 0, err
	}
	result.CWD, err = os.Readlink(filepath.Join(base, "cwd"))
	if err != nil {
		return result, 0, err
	}
	result.CWD = filepath.Clean(result.CWD)
	command, err := os.ReadFile(filepath.Join(base, "comm"))
	if err != nil {
		return result, 0, err
	}
	result.Command = strings.TrimSpace(string(command))
	return result, uint32(uid), nil
}
func (g *Guard) allowed(v listener, uid uint32) bool {
	if within(g.Root, v.CWD) {
		return uid == g.UID || g.managed(v.PID)
	}
	return within("/workspace", v.CWD) && g.managed(v.PID)
}
func (g *Guard) once(ctx context.Context, port int) ([]listener, error) {
	inodes := map[string]bool{}
	for _, table := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(filepath.Join(g.procRoot(), "net", table))
		if err != nil {
			return nil, errors.New("unable to inspect listening sockets")
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) <= 9 || fields[3] != "0A" {
				continue
			}
			_, hexPort, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			number, err := strconv.ParseInt(hexPort, 16, 32)
			if err == nil && int(number) == port {
				inodes[fields[9]] = true
			}
		}
	}
	result := []listener{}
	if len(inodes) == 0 {
		return result, nil
	}
	entries, err := os.ReadDir(g.procRoot())
	if err != nil {
		return nil, err
	}
	matched := map[string]bool{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		v, uid, err := g.identity(pid)
		if err != nil || !g.allowed(v, uid) {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(g.procRoot(), entry.Name(), "fd"))
		if err != nil {
			continue
		}
		owns := false
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(g.procRoot(), entry.Name(), "fd", fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if inodes[inode] {
				matched[inode] = true
				owns = true
			}
		}
		if owns {
			result = append(result, v)
		}
	}
	if len(matched) != len(inodes) {
		return nil, fmt.Errorf("listener details are unavailable or owned by another user (%d/%d sockets matched)", len(matched), len(inodes))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].PID < result[j].PID })
	return result, nil
}
func (g *Guard) listeners(ctx context.Context, port int) ([]listener, error) {
	if err := Validate(port); err != nil {
		return nil, err
	}
	var result []listener
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		result, err = g.once(ctx, port)
		if err == nil || !strings.HasPrefix(err.Error(), "listener details are unavailable") {
			return result, err
		}
		if attempt < 4 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 25 * time.Millisecond):
			}
		}
	}
	return nil, err
}
func (g *Guard) Inspect(ctx context.Context, port int) (map[string]any, error) {
	listeners, err := g.listeners(ctx, port)
	if err != nil {
		return nil, err
	}
	rows := []map[string]any{}
	for _, v := range listeners {
		cwd := v.CWD
		if within(g.Root, cwd) {
			rel, _ := filepath.Rel(g.Root, cwd)
			cwd = filepath.Join("/workspace", rel)
		}
		rows = append(rows, map[string]any{"pid": v.PID, "cwd": cwd, "command": v.Command, "local_address": fmt.Sprintf("*:%d", port)})
	}
	return map[string]any{"port": port, "in_use": len(rows) > 0, "listeners": rows}, nil
}
func (g *Guard) Stop(ctx context.Context, port int) (map[string]any, error) {
	listeners, err := g.listeners(ctx, port)
	if err != nil {
		return nil, err
	}
	type target struct {
		listener
		fd int
	}
	targets := []target{}
	defer func() {
		for _, v := range targets {
			_ = unix.Close(v.fd)
		}
	}()
	// Pin every process before signaling any of them, then recheck ownership.
	for _, v := range listeners {
		fd, err := unix.PidfdOpen(v.PID, 0)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target{v, fd})
		current, uid, err := g.identity(v.PID)
		if err != nil || current.Started != v.Started || !g.allowed(current, uid) {
			return nil, errors.New("listener changed during validation")
		}
	}
	pids := []int{}
	for _, v := range targets {
		if err := unix.PidfdSendSignal(v.fd, unix.SIGTERM, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			return nil, err
		}
		pids = append(pids, v.PID)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, v := range targets {
		poll := []unix.PollFd{{Fd: int32(v.fd), Events: unix.POLLIN}}
		for time.Now().Before(deadline) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n, err := unix.Poll(poll, 100)
			if err != nil && !errors.Is(err, unix.EINTR) {
				return nil, err
			}
			if n > 0 {
				break
			}
		}
		if poll[0].Revents&unix.POLLIN != 0 {
			continue
		}
		current, uid, err := g.identity(v.PID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || current.Started != v.Started || !g.allowed(current, uid) {
			return nil, errors.New("listener changed during validation")
		}
		if err := unix.PidfdSendSignal(v.fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
			return nil, err
		}
	}
	return map[string]any{"port": port, "stopped": len(pids) > 0, "terminated_pids": pids}, nil
}
