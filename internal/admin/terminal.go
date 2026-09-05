package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
)

func ReadSecret(ctx context.Context, prompt io.Writer) (string, error) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", fault.Error("secret input requires an interactive terminal")
	}
	defer terminal.Close()
	return readTerminal(ctx, terminal, prompt)
}

func readTerminal(ctx context.Context, terminal *os.File, prompt io.Writer) (value string, err error) {
	fd := int(terminal.Fd())
	original, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return "", fault.Error("secret input requires a terminal")
	}
	hidden := *original
	hidden.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON
	hidden.Cc[unix.VMIN] = 1
	hidden.Cc[unix.VTIME] = 0
	if err = unix.IoctlSetTermios(fd, unix.TCSETS, &hidden); err != nil {
		return "", err
	}
	defer func() {
		restoreErr := unix.IoctlSetTermios(fd, unix.TCSETS, original)
		fmt.Fprintln(prompt)
		if err == nil && restoreErr != nil {
			value = ""
			err = restoreErr
		}
	}()
	if _, err = fmt.Fprint(prompt, "Secret value: "); err != nil {
		return "", err
	}
	data := make([]byte, 0, 128)
	defer func() { clear(data) }()
	for {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		_, err = unix.Poll(fds, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return "", err
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			return "", io.EOF
		}
		if fds[0].Revents&unix.POLLIN == 0 {
			continue
		}
		var next [1]byte
		n, readErr := unix.Read(fd, next[:])
		if readErr != nil {
			return "", readErr
		}
		if n == 0 {
			return "", io.EOF
		}
		switch next[0] {
		case '\n', '\r':
			if !utf8.Valid(data) {
				return "", fault.Error("secret input must be UTF-8")
			}
			return string(data), nil
		case 4:
			return "", io.EOF
		case 8, 127:
			if len(data) > 0 {
				_, size := utf8.DecodeLastRune(data)
				clear(data[len(data)-size:])
				data = data[:len(data)-size]
			}
		case 21:
			clear(data)
			data = data[:0]
		default:
			if len(data) >= 65536 {
				return "", fault.Error("secret input exceeds 65536 bytes")
			}
			data = append(data, next[0])
		}
	}
}
