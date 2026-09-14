// Package process owns bounded subprocess execution and lifecycle management.
package process

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

type Result struct {
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Raw       []byte `json:"-"`
}
type Spec struct {
	Argv      []string
	CWD       string
	Env       []string
	Identity  *Identity
	Input     []byte
	Timeout   time.Duration
	MaxOutput int
}

// Identity drops a privileged parent to the configured service account before
// exec. UID and GID zero are rejected because this boundary is for delegation.
type Identity struct {
	UID, GID uint32
	Groups   []uint32
}

func (i *Identity) validate() error {
	if i == nil {
		return nil
	}
	if i.UID == 0 || i.GID == 0 {
		return errors.New("delegated process identity must be unprivileged")
	}
	for _, group := range i.Groups {
		if group == 0 {
			return errors.New("delegated process groups must be unprivileged")
		}
	}
	return nil
}

type Buffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func NewBuffer(limit int) *Buffer { return &Buffer{limit: max(0, limit)} }
func (b *Buffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	remaining := b.limit - len(b.data)
	if n > remaining {
		b.truncated = true
	}
	b.data = append(b.data, data[:min(n, remaining)]...)
	return n, nil
}
func (b *Buffer) Snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data), b.truncated
}

func Environment() []string {
	return []string{"HOME=/home/runner", "GH_CONFIG_DIR=/home/runner/.config/gh", "TMPDIR=/tmp", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0"}
}

func Command(ctx context.Context, spec Spec) *exec.Cmd {
	env := spec.Env
	if env == nil {
		env = Environment()
	}
	var argv0 string
	var lookupErr error
	if len(spec.Argv) == 0 {
		lookupErr = errors.New("missing executable")
	} else {
		argv0, lookupErr = executablePath(spec.Argv[0], spec.CWD, env)
	}
	var args []string
	if len(spec.Argv) > 0 {
		args = spec.Argv[1:]
	}
	cmd := exec.CommandContext(ctx, argv0, args...)
	if lookupErr != nil {
		cmd.Err = lookupErr
	}
	identityErr := spec.Identity.validate()
	if identityErr != nil && cmd.Err == nil {
		cmd.Err = identityErr
	}
	if len(spec.Argv) > 0 {
		cmd.Args[0] = spec.Argv[0]
	}
	cmd.Dir, cmd.Env = spec.CWD, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if spec.Identity != nil && identityErr == nil {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid: spec.Identity.UID, Gid: spec.Identity.GID,
			Groups: slices.Clone(spec.Identity.Groups),
		}
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	return cmd
}

func Run(ctx context.Context, spec Spec) (Result, error) {
	if len(spec.Argv) == 0 {
		return Result{}, errors.New("missing executable")
	}
	if spec.Timeout <= 0 {
		spec.Timeout = 30 * time.Second
	}
	if spec.MaxOutput <= 0 {
		spec.MaxOutput = 262144
	}
	ctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	cmd := Command(ctx, spec)
	if spec.Input != nil {
		cmd.Stdin = bytes.NewReader(spec.Input)
	}
	buffer := NewBuffer(spec.MaxOutput)
	cmd.Stdout = buffer
	cmd.Stderr = buffer
	err := cmd.Run()
	raw, truncated := buffer.Snapshot()
	output, clipped := boundedText(raw, spec.MaxOutput, truncated)
	result := Result{Raw: raw, Output: output, Truncated: clipped}
	if ctx.Err() != nil {
		result.ExitCode = 124
		result.TimedOut = true
		return result, nil
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return result, err
		}
		result.ExitCode = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			result.ExitCode = -int(status.Signal())
		}
	}
	return result, nil
}

// boundedText mirrors Python UTF-8 replacement decoding followed by a byte
// limit. An invalid byte in the middle must not discard the valid suffix.
func boundedText(raw []byte, limit int, truncated bool) (string, bool) {
	var text strings.Builder
	for len(raw) > 0 {
		r, size := utf8.DecodeRune(raw)
		if r == utf8.RuneError && size == 1 {
			width := 1
			switch {
			case raw[0] >= 0xc2 && raw[0] <= 0xdf:
				width = 2
			case raw[0] >= 0xe0 && raw[0] <= 0xef:
				width = 3
			case raw[0] >= 0xf0 && raw[0] <= 0xf4:
				width = 4
			}
			for size < width && size < len(raw) {
				b := raw[size]
				if b < 0x80 || b > 0xbf || size == 1 && (raw[0] == 0xe0 && b < 0xa0 || raw[0] == 0xed && b > 0x9f || raw[0] == 0xf0 && b < 0x90 || raw[0] == 0xf4 && b > 0x8f) {
					break
				}
				size++
			}
			if truncated && size < width && size == len(raw) {
				break // The raw buffer split a potentially valid final rune.
			}
		}
		if text.Len()+utf8.RuneLen(r) > limit {
			truncated = true
			break
		}
		text.WriteRune(r)
		raw = raw[size:]
	}
	return text.String(), truncated
}
