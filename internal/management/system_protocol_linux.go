package management

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	managedcommand "loki/internal/platform/command"
)

type systemRequest struct {
	Args []string `json:"args"`
}

// Requests cross an administrator boundary. Acquisitions accept only official
// catalogs through the CLI's verified release path; caller-supplied archives,
// filesystem credentials and remote commands are not administrator operations.
func ValidateSystemArguments(args []string) ([]string, bool, error) {
	var clean []string
	for _, arg := range args {
		if len(arg) > 4096 || strings.ContainsAny(arg, "\x00\r\n") {
			return nil, false, fmt.Errorf("invalid system request argument")
		}
		if arg == "--json" || arg == "--json=true" || arg == "--json=false" {
			continue
		}
		clean = append(clean, arg)
	}
	if len(clean) == 0 || len(clean) > 32 {
		return nil, false, fmt.Errorf("invalid system command")
	}
	if clean[0] == "--root" && len(clean) > 1 {
		clean = clean[2:]
	}
	if len(clean) == 0 {
		return nil, false, fmt.Errorf("invalid system command")
	}
	// Global execution/root options are supplied by the service, never callers.
	for _, arg := range clean {
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		for _, forbidden := range []string{"root", "host", "distribution", "address", "remote-command", "remote-user", "system-socket", "catalog", "archives", "config-file", "private-key-file", "key-file", "bin-dir"} {
			if name == forbidden {
				return nil, false, fmt.Errorf("%s is outside system-host authority", forbidden)
			}
		}
	}
	input := false
	switch clean[0] {
	case "status", "doctor", "version", "backup", "rollback", "_prepare-environment":
		if len(clean) != 1 {
			return nil, false, fmt.Errorf("invalid system operator command")
		}
	case "uninstall":
		if len(clean) != 1 && !(len(clean) == 2 && clean[1] == "--purge-data") {
			return nil, false, fmt.Errorf("invalid system removal request")
		}
	case "restore":
		if len(clean) != 2 || !backupIDPattern.MatchString(clean[1]) {
			return nil, false, fmt.Errorf("invalid system restore")
		}
	case "backups":
		if !(len(clean) == 1 || len(clean) == 2 && clean[1] == "list" || len(clean) == 3 && clean[1] == "remove" && backupIDPattern.MatchString(clean[2])) {
			return nil, false, fmt.Errorf("invalid system backup command")
		}
	case "_connection-state":
		if len(clean) != 3 || clean[1] != "--workspace" {
			return nil, false, fmt.Errorf("invalid system connection probe")
		}
	case "_github-setup-relay":
		if len(clean) != 1 {
			return nil, false, fmt.Errorf("invalid protected setup request")
		}
		input = true
	case "setup":
		for i := 1; i < len(clean); i++ {
			arg := clean[i]
			if arg == "--no-start" {
				continue
			}
			if arg == "--tools" || arg == "--mode" || arg == "--version" {
				i++
				if i >= len(clean) {
					return nil, false, fmt.Errorf("missing setup value")
				}
				continue
			}
			return nil, false, fmt.Errorf("unsupported system setup option")
		}
	case "tools":
		if len(clean) < 2 {
			return nil, false, fmt.Errorf("missing system tool operation")
		}
		switch clean[1] {
		case "serve":
			if len(clean) != 2 {
				return nil, false, fmt.Errorf("system tools serve uses the selected full composition")
			}
			input = true
		case "start", "stop", "restart", "status", "doctor", "list", "recover", "topology", "layouts", "plan", "resources":
			if len(clean) != 2 {
				return nil, false, fmt.Errorf("invalid system tool operation")
			}
		case "install", "update", "enable", "disable", "remove", "prune": // Each leaf parser validates tool names, releases and capabilities.
		default:
			return nil, false, fmt.Errorf("unsupported system tool operation")
		}
	case "integrations":
		if len(clean) < 3 {
			return nil, false, fmt.Errorf("invalid system integration command")
		}
		if clean[2] != "git" && clean[2] != "github" {
			return nil, false, fmt.Errorf("unsupported system integration")
		}
		if clean[1] != "setup" && clean[1] != "status" && clean[1] != "doctor" && clean[1] != "refresh" {
			return nil, false, fmt.Errorf("unsupported system integration operation")
		}
		if clean[1] == "setup" {
			input = true
		}
	default:
		return nil, false, fmt.Errorf("unsupported system command")
	}
	// Keep formatting flags; remove the transport root after enforcing the fixed
	// service root. The caller cannot choose another root through this socket.
	var normalized []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--root" && i+1 < len(args) {
			i++
			continue
		}
		normalized = append(normalized, args[i])
	}
	return normalized, input, nil
}

type systemFrameWriter struct {
	mu     *sync.Mutex
	out    io.Writer
	kind   byte
	cancel context.CancelFunc
}

func (w systemFrameWriter) Write(data []byte) (count int, failure error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer func() {
		if failure != nil && w.cancel != nil {
			w.cancel()
		}
	}()
	written := 0
	for len(data) > 0 {
		part := data[:min(len(data), 64<<10)]
		var header [5]byte
		header[0] = w.kind
		binary.BigEndian.PutUint32(header[1:], uint32(len(part)))
		if _, err := w.out.Write(header[:]); err != nil {
			return written, err
		}
		n, err := w.out.Write(part)
		written += n
		if err != nil {
			return written, err
		}
		if n != len(part) {
			return written, io.ErrShortWrite
		}
		data = data[len(part):]
	}
	return written, nil
}

func serveSystemRequest(ctx context.Context, connection *net.UnixConn, binaryPath, root string, uid uint32) {
	defer connection.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopClose()
	var peer *unix.Ucred
	raw, err := connection.SyscallConn()
	if err != nil {
		return
	}
	if err := raw.Control(func(fd uintptr) { peer, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil || peer == nil || peer.Uid != uid {
		return
	}
	reader := bufio.NewReaderSize(connection, 64<<10)
	_ = connection.SetReadDeadline(time.Now().Add(15 * time.Second))
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	var request systemRequest
	var mutex sync.Mutex
	out := systemFrameWriter{mu: &mutex, out: connection, kind: 1, cancel: cancel}
	diagnostics := systemFrameWriter{mu: &mutex, out: connection, kind: 2, cancel: cancel}
	exit := systemFrameWriter{mu: &mutex, out: connection, kind: 3, cancel: cancel}
	if json.Unmarshal(line, &request) != nil {
		return
	}
	args, input, err := ValidateSystemArguments(request.Args)
	if err != nil {
		_, _ = fmt.Fprintln(diagnostics, err)
		_, _ = exit.Write([]byte("1"))
		return
	}
	if !input {
		var deadline context.CancelFunc
		ctx, deadline = context.WithTimeout(ctx, 20*time.Minute)
		defer deadline()
	}
	command := managedcommand.New(ctx, binaryPath, append([]string{"--host", "local", "--root", root}, args...)...)
	command.Stdout, command.Stderr = out, diagnostics
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME=/root", "LANG=C.UTF-8", "LC_ALL=C.UTF-8"}
	var pipe io.WriteCloser
	if input {
		pipe, err = command.StdinPipe()
		if err != nil {
			return
		}
	}
	if err := command.Start(); err != nil {
		_, _ = fmt.Fprintln(diagnostics, "system host command could not start")
		_, _ = exit.Write([]byte("1"))
		return
	}
	stop, err := managedcommand.Own(command)
	if err != nil {
		cancel()
		_ = command.Wait()
		return
	}
	defer stop()
	if input {
		go func() {
			_, err := io.Copy(pipe, reader)
			_ = pipe.Close()
			if err != nil {
				cancel()
			}
		}()
	}
	err = command.Wait()
	code := 0
	if err != nil {
		code = 1
		if failure, ok := err.(*exec.ExitError); ok {
			code = failure.ExitCode()
		}
	}
	_, _ = exit.Write([]byte(strconv.Itoa(code)))
}

func ServeSystemHost(ctx context.Context, listener *net.UnixListener, binaryPath, root string, uid uint32) error {
	context.AfterFunc(ctx, func() { _ = listener.Close() })
	var wait sync.WaitGroup
	slots := make(chan struct{}, 16)
	defer wait.Wait()
	for {
		connection, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			connection.Close()
			return nil
		default:
			connection.Close()
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			defer func() { <-slots }()
			serveSystemRequest(ctx, connection, binaryPath, root, uid)
		}()
	}
}

func SystemRelay(ctx context.Context, socket string, args []string, input io.Reader, out, diagnostics io.Writer) error {
	var stat unix.Stat_t
	if err := unix.Lstat(socket, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFSOCK || stat.Uid != 0 {
		return fmt.Errorf("managed system socket is unavailable; retry loki setup")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer connection.Close()
	context.AfterFunc(ctx, func() { _ = connection.Close() })
	args, needsInput, err := ValidateSystemArguments(args)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(connection).Encode(systemRequest{Args: args}); err != nil {
		return err
	}
	if needsInput {
		go func() {
			_, _ = io.Copy(connection, input)
			if unix, ok := connection.(*net.UnixConn); ok {
				_ = unix.CloseWrite()
			}
		}()
	} else {
		if unix, ok := connection.(*net.UnixConn); ok {
			_ = unix.CloseWrite()
		}
	}
	for {
		var header [5]byte
		if _, err := io.ReadFull(connection, header[:]); err != nil {
			return fmt.Errorf("system host response ended before completion: %w", err)
		}
		size := binary.BigEndian.Uint32(header[1:])
		if size > 16<<20 {
			return fmt.Errorf("system response exceeded its bound")
		}
		switch header[0] {
		case 1, 2:
			destination := out
			if header[0] == 2 {
				destination = diagnostics
			}
			if _, err := io.CopyN(destination, connection, int64(size)); err != nil {
				return err
			}
		case 3:
			if size > 16 {
				return fmt.Errorf("invalid system exit response")
			}
			data := make([]byte, size)
			if _, err := io.ReadFull(connection, data); err != nil {
				return err
			}
			code, err := strconv.Atoi(string(data))
			if err != nil || code != 0 {
				return fmt.Errorf("system host command failed (exit %s)", data)
			}
			return nil
		default:
			return fmt.Errorf("invalid system response channel")
		}
	}
}
