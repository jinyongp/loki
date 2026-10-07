package management

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	managedcommand "loki/internal/platform/command"
)

type boundedDockerOutput struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedDockerOutput) Write(data []byte) (int, error) {
	n := len(data)
	if n > b.limit-b.Len() {
		b.overflow = true
	}
	if available := b.limit - b.Len(); available > 0 {
		_, _ = b.Buffer.Write(data[:min(available, len(data))])
	}
	return n, nil
}

type DockerFullBackend struct {
	Store          Store
	Binary, Socket string
	Diagnostics    io.Writer
	Sudo           string
}

type dockerCommandFailure struct {
	operation, diagnostics string
	cause                  error
}

func (e *dockerCommandFailure) Error() string {
	return fmt.Sprintf("%s failed: %v: %s", e.operation, e.cause, e.diagnostics)
}
func (e *dockerCommandFailure) Unwrap() error { return e.cause }

func NewDockerFullBackend(store Store, socket string, diagnostics io.Writer) (*DockerFullBackend, error) {
	if socket == "" {
		socket = "/var/run/docker.sock"
	}
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket {
		return nil, fmt.Errorf("full backend requires an explicit local Docker socket")
	}
	var stat unix.Stat_t
	if err := unix.Stat(socket, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Uid != 0 {
		return nil, fmt.Errorf("full mode requires the local administrator-owned Docker socket")
	}
	binary, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("full mode requires Docker on the selected Linux host; install its declared prerequisites first")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	if diagnostics == nil {
		diagnostics = io.Discard
	}
	backend := &DockerFullBackend{Store: store, Binary: binary, Socket: socket, Diagnostics: diagnostics}
	if os.Geteuid() != 0 && unix.Access(socket, unix.R_OK|unix.W_OK) != nil {
		backend.Sudo, err = exec.LookPath("sudo")
		if err != nil {
			return nil, fmt.Errorf("Docker administration is not authorized; run loki setup to prepare this host")
		}
	}
	return backend, nil
}

func (b *DockerFullBackend) dockerCommand(args ...string) []string {
	command := append([]string{b.Binary, "--host", "unix://" + b.Socket}, args...)
	if b.Sudo != "" {
		command = append([]string{b.Sudo, "-n"}, command...)
	}
	return command
}

// Arguments remain separate data, and the daemon endpoint is always explicit.
// Only bounded output is retained. Long acquisition operations report elapsed
// time without exposing environment, private configuration or command input.
func (b *DockerFullBackend) command(ctx context.Context, label string, input io.Reader, args ...string) ([]byte, error) {
	if !filepath.IsAbs(b.Binary) || !filepath.IsAbs(b.Socket) {
		return nil, fmt.Errorf("invalid owned Docker backend")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	argv := b.dockerCommand(args...)
	command := managedcommand.New(ctx, argv[0], argv[1:]...)
	environment := []string{}
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key != "DOCKER_HOST" && key != "DOCKER_CONTEXT" && key != "DOCKER_API_VERSION" {
			environment = append(environment, item)
		}
	}
	command.Env = append(environment, "DOCKER_API_VERSION=1.47")
	stdout, stderr := &boundedDockerOutput{limit: 1 << 20}, &boundedDockerOutput{limit: 64 << 10}
	command.Stdin, command.Stdout, command.Stderr = input, stdout, stderr
	command.WaitDelay = 2 * time.Second
	if err := command.Start(); err != nil {
		return nil, err
	}
	cleanup, err := managedcommand.Own(command)
	if err != nil {
		managedcommand.Cleanup(command)
		_ = command.Wait()
		return nil, err
	}
	defer cleanup()
	stop, done := make(chan struct{}), make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fmt.Fprintf(b.Diagnostics, "Still %s (%ds elapsed)...\n", label, int(time.Since(started).Seconds()))
			}
		}
	}()
	err = command.Wait()
	close(stop)
	<-done
	if err != nil {
		return nil, &dockerCommandFailure{operation: label, diagnostics: strings.TrimSpace(stderr.String()), cause: err}
	}
	if stdout.overflow || stderr.overflow {
		return nil, fmt.Errorf("%s exceeded the bounded Docker diagnostic output", label)
	}
	return stdout.Bytes(), nil
}
