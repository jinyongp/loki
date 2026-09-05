package process

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/redact"
)

type ManagerOptions struct {
	MaxProcesses, MaxOutputBytes     int
	Retention, StopGrace, DrainGrace time.Duration
	RequireRedactor                  bool
}

type StartSpec struct {
	Spec
	Name               string
	Group, InstanceKey *string
	MaxGroupProcesses  *int
	// Metadata must contain public data only. It is copied at admission.
	Metadata map[string]any
	// Redactor is mandatory for any caller injecting private environment values.
	Redactor *redact.Filter
	// Files are explicitly owned by the caller until Start returns. Nil stdin
	// becomes /dev/null; private launches may supply a sealed credential input.
	Stdin      *os.File
	ExtraFiles []*os.File
	// Cleanup is owned by the manager only after successful non-reused admission.
	// It runs once after process exit and output draining, before Stop/Close return.
	// The callback must be bounded and may not re-enter this manager.
	Cleanup func() error
}

// Manager owns process lifetimes independently of individual RPC requests.
// Its owner must close it during service shutdown. Commands run in a new
// session; the service sandbox remains responsible for containing deliberate
// escapes from that session (for example, a child calling setsid).
type Manager struct {
	mu        sync.Mutex
	options   ManagerOptions
	processes []*managedProcess
	closed    bool
}

type managedProcess struct {
	mu                     sync.Mutex
	cmd                    *exec.Cmd
	id, name               string
	group, instanceKey     *string
	metadata               json.RawMessage
	startedAt, completedAt time.Time
	data                   []byte
	base                   int64
	maximum                int
	redactor               *redact.Filter
	exited, timedOut       bool
	exitCode               int
	cleanupError           string
	cleanupPending         bool
	exitedCh, done         chan struct{}
}

func NewManager(options ManagerOptions) (*Manager, error) {
	if options.MaxProcesses < 1 || options.MaxProcesses > 32 || options.MaxOutputBytes < 1 || options.MaxOutputBytes > 16777216 || options.Retention <= 0 {
		return nil, errors.New("invalid process manager limits")
	}
	if options.StopGrace <= 0 {
		options.StopGrace = 5 * time.Second
	}
	if options.DrainGrace <= 0 {
		options.DrainGrace = time.Second
	}
	return &Manager{options: options}, nil
}

func (m *Manager) Start(spec StartSpec) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, fault.Error("process manager is closed")
	}
	if m.options.RequireRedactor && spec.Redactor == nil {
		return nil, errors.New("private process requires an output redactor")
	}
	m.pruneLocked()
	running, groupRunning := 0, 0
	for _, p := range m.processes {
		if !p.running() {
			continue
		}
		if spec.InstanceKey != nil && p.instanceKey != nil && *spec.InstanceKey == *p.instanceKey {
			result := p.snapshot(nil, 0)
			result["reused"] = true
			return result, nil
		}
		running++
		if spec.Group != nil && p.group != nil && *spec.Group == *p.group {
			groupRunning++
		}
	}
	if running >= m.options.MaxProcesses {
		detail := fmt.Sprintf("total %d/%d", running, m.options.MaxProcesses)
		if spec.Group != nil && spec.MaxGroupProcesses != nil {
			detail += fmt.Sprintf(", profile %s %d/%d", *spec.Group, groupRunning, *spec.MaxGroupProcesses)
		}
		return nil, fault.Error("maximum concurrent process count reached (" + detail + ")")
	}
	if spec.Group != nil && spec.MaxGroupProcesses != nil && groupRunning >= *spec.MaxGroupProcesses {
		return nil, fault.Error(fmt.Sprintf("maximum concurrent process count reached for profile %s (%d/%d; total %d/%d)", *spec.Group, groupRunning, *spec.MaxGroupProcesses, running, m.options.MaxProcesses))
	}
	if len(spec.Argv) == 0 || spec.Argv[0] == "" || spec.Timeout <= 0 || spec.MaxOutput < 1 || spec.MaxOutput > 67108864 || spec.Input != nil {
		return nil, fault.Error("invalid process command or limits")
	}
	metadata, err := json.Marshal(spec.Metadata)
	if err != nil {
		return nil, errors.New("invalid process metadata")
	}
	var extra map[string]json.RawMessage
	if err = json.Unmarshal(metadata, &extra); err != nil {
		return nil, err
	}
	// Lifecycle fields belong to this manager, not to its callers.
	for _, key := range []string{"session_id", "name", "status", "exit_code", "timed_out", "started_at", "output", "offset", "next_offset", "available_from", "output_lost", "has_more", "reused", "cleanup_error"} {
		if _, exists := extra[key]; exists {
			return nil, errors.New("reserved process metadata field")
		}
	}
	var id [12]byte
	if _, err = rand.Read(id[:]); err != nil {
		return nil, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	env := slices.Clone(spec.Env)
	if spec.Env == nil {
		env = Environment()
	}
	executable, err := executablePath(spec.Argv[0], spec.CWD, env)
	if err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	cmd := exec.Command(executable, spec.Argv[1:]...)
	cmd.Args[0] = spec.Argv[0]
	cmd.Dir, cmd.Env = spec.CWD, env
	cmd.ExtraFiles = slices.Clone(spec.ExtraFiles)
	if spec.Stdin != nil {
		cmd.Stdin = spec.Stdin
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = writer, writer
	// Nil stdin deliberately becomes /dev/null, never the server's stdin.
	if err = cmd.Start(); err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	writer.Close()
	copyString := func(value *string) *string {
		if value == nil {
			return nil
		}
		v := *value
		return &v
	}
	p := &managedProcess{cmd: cmd, id: base64.RawURLEncoding.EncodeToString(id[:]), name: spec.Name,
		group: copyString(spec.Group), instanceKey: copyString(spec.InstanceKey), metadata: metadata,
		startedAt: time.Now().UTC(), maximum: spec.MaxOutput, redactor: spec.Redactor, cleanupPending: spec.Cleanup != nil, exitedCh: make(chan struct{}), done: make(chan struct{})}
	m.processes = append(m.processes, p)
	go p.wait(reader, m.options.DrainGrace, spec.Cleanup)
	go func() {
		timer := time.NewTimer(spec.Timeout)
		defer timer.Stop()
		select {
		case <-p.exitedCh:
			return
		case <-timer.C:
			p.mu.Lock()
			if p.exited {
				p.mu.Unlock()
				return
			}
			p.timedOut = true
			p.mu.Unlock()
			p.terminate(m.options.StopGrace)
		}
	}()
	return p.snapshot(nil, 0), nil
}

func (p *managedProcess) Write(chunk []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(chunk)
	retained := p.maximum + p.redactor.Context()
	if n >= retained {
		p.base += int64(len(p.data) + n - retained)
		p.data = append(p.data[:0], chunk[n-retained:]...)
	} else {
		if overflow := len(p.data) + n - retained; overflow > 0 {
			copy(p.data, p.data[overflow:])
			p.data = p.data[:len(p.data)-overflow]
			p.base += int64(overflow)
		}
		p.data = append(p.data, chunk...)
	}
	return n, nil
}

func (p *managedProcess) wait(reader *os.File, drainGrace time.Duration, cleanup func() error) {
	drained := make(chan struct{})
	go func() { _, _ = io.Copy(p, reader); reader.Close(); close(drained) }()
	// WNOWAIT retains the unreaped leader's PID while descendants are killed.
	// This prevents an old session from signalling a recycled process-group ID.
	var info unix.Siginfo
	var waitErr error
	for {
		waitErr = unix.Waitid(unix.P_PID, p.cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(waitErr, unix.EINTR) {
			break
		}
	}
	p.mu.Lock()
	if !errors.Is(waitErr, unix.ECHILD) {
		_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGKILL)
	}
	err := p.cmd.Wait()
	p.exitCode = 0
	if err != nil {
		p.exitCode = -1
		if p.cmd.ProcessState != nil {
			p.exitCode = p.cmd.ProcessState.ExitCode()
			if status, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				p.exitCode = -int(status.Signal())
			}
		}
	}
	p.exited = true
	close(p.exitedCh)
	p.mu.Unlock()
	timer := time.NewTimer(drainGrace)
	defer timer.Stop()
	select {
	case <-drained:
	case <-timer.C:
		reader.Close()
		<-drained
	}
	var cleanupError string
	if cleanup != nil {
		if err := cleanup(); err != nil {
			cleanupError = fault.Public(err)
		}
	}
	p.mu.Lock()
	p.cleanupError = cleanupError
	p.cleanupPending = false
	p.completedAt = time.Now()
	close(p.done)
	p.mu.Unlock()
}

func (p *managedProcess) terminate(grace time.Duration) {
	p.mu.Lock()
	if !p.exited {
		_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGTERM)
	}
	p.mu.Unlock()
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-p.exitedCh:
	case <-timer.C:
		p.mu.Lock()
		if !p.exited {
			_ = unix.Kill(-p.cmd.Process.Pid, unix.SIGKILL)
		}
		p.mu.Unlock()
	}
	<-p.done
}

func (p *managedProcess) running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.exited || p.cleanupPending
}

func (p *managedProcess) snapshot(offset *int64, limit int) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	available := p.base + int64(max(0, len(p.data)-p.maximum))
	requested := available
	if offset != nil {
		requested = max(0, *offset)
	}
	actual := max(requested, available)
	index := int(min(actual-p.base, int64(len(p.data))))
	end := min(len(p.data), index+max(0, limit))
	chunk := p.redactor.Slice(p.data, index, end)
	next := actual + int64(end-index)
	// Python's errors="replace" may expand invalid bytes beyond the read limit.
	output, _ := boundedText(chunk, 3*len(chunk), false)
	var exitCode any
	status := "running"
	if p.exited && !p.cleanupPending {
		status, exitCode = "exited", p.exitCode
	}
	timestamp := p.startedAt.Format("2006-01-02T15:04:05.000000+00:00")
	if p.startedAt.Nanosecond()/1000 == 0 {
		timestamp = p.startedAt.Format("2006-01-02T15:04:05+00:00")
	}
	result := map[string]any{"session_id": p.id, "name": p.name, "status": status,
		"exit_code": exitCode, "timed_out": p.timedOut,
		"started_at": timestamp,
		"output":     output, "offset": actual, "next_offset": next, "available_from": available,
		"output_lost": requested < available, "has_more": next < p.base+int64(len(p.data))}
	if p.cleanupError != "" {
		result["cleanup_error"] = p.cleanupError
	}
	var extra map[string]any
	decoder := json.NewDecoder(bytes.NewReader(p.metadata))
	decoder.UseNumber()
	_ = decoder.Decode(&extra)
	for key, value := range extra {
		result[key] = value
	}
	return result
}

func (m *Manager) get(id string) (*managedProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	for _, p := range m.processes {
		if p.id == id {
			return p, nil
		}
	}
	return nil, fault.Error("unknown process session")
}

func (m *Manager) Read(id string, offset *int64, limit int) (map[string]any, error) {
	p, err := m.get(id)
	if err != nil {
		return nil, err
	}
	return p.snapshot(offset, max(1, min(limit, m.options.MaxOutputBytes))), nil
}

func (m *Manager) Stop(id string) (map[string]any, error) {
	p, err := m.get(id)
	if err != nil {
		return nil, err
	}
	p.terminate(m.options.StopGrace)
	return p.snapshot(nil, 0), nil
}

func (m *Manager) List() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	items := make([]map[string]any, 0, len(m.processes))
	for _, p := range m.processes {
		items = append(items, p.snapshot(nil, 0))
	}
	return map[string]any{"processes": items}
}

func (m *Manager) Usage() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	count, groups := 0, map[string]int{}
	for _, p := range m.processes {
		if p.running() {
			count++
			if p.group != nil {
				groups[*p.group]++
			}
		}
	}
	return map[string]any{"total": count, "groups": groups}
}

func (m *Manager) FindRunning(key string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	for _, p := range m.processes {
		if p.instanceKey != nil && *p.instanceKey == key && p.running() {
			result := p.snapshot(nil, 0)
			result["reused"] = true
			return result
		}
	}
	return nil
}

func (m *Manager) pruneLocked() {
	cutoff := time.Now().Add(-m.options.Retention)
	completed := make(map[*managedProcess]time.Time)
	m.processes = slices.DeleteFunc(m.processes, func(p *managedProcess) bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.completedAt.IsZero() {
			return false
		}
		completed[p] = p.completedAt
		return p.completedAt.Before(cutoff)
	})
	for len(m.processes) > 32 {
		index := -1
		for i, p := range m.processes {
			if timestamp, ok := completed[p]; ok && (index == -1 || timestamp.Before(completed[m.processes[index]])) {
				index = i
			}
		}
		if index == -1 {
			break
		}
		m.processes = slices.Delete(m.processes, index, index+1)
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	items := slices.Clone(m.processes)
	m.mu.Unlock()
	var group sync.WaitGroup
	for _, p := range items {
		group.Go(func() { p.terminate(min(m.options.StopGrace, 3*time.Second)) })
	}
	group.Wait()
}
