//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/fault"
)

func runSecretProtection(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "secret protection requires the host administrator")
		return 1
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: loki secret protection status|init|unlock|lock|recover|rewrap [OPTIONS]")
		return 2
	}
	action := args[0]
	if action == "rewrap" {
		return runSecretRewrap(args[1:], stdin, stdout, stderr)
	}
	needsInput := false
	switch action {
	case "init", "unlock":
		needsInput = true
	case "status", "lock", "recover":
	default:
		fmt.Fprintln(stderr, "unknown secret protection action")
		return 2
	}
	flags := flag.NewFlagSet("secret protection", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layoutPath := flags.String("layout", "/etc/loki-go/secrets-admin.json", "installed private administrator channel layout")
	file := flags.String("file", "", "owned mode-0600 passphrase file, preserved after use")
	fromStdin := flags.Bool("stdin", false, "read the passphrase through private stdin")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || *file != "" && *fromStdin || !needsInput && (*file != "" || *fromStdin) {
		return 2
	}
	var layout applicationClientLayout
	if daemon.ReadJSON(*layoutPath, &layout) != nil || layout.UID == 0 {
		fmt.Fprintln(stderr, "installed secret protection channel is unavailable")
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var body []byte
	if needsInput {
		var err error
		body, err = readProtectionInput(ctx, stdin, stderr, *file, *fromStdin)
		if err != nil {
			fmt.Fprintln(stderr, "passphrase input is unavailable or invalid")
			return 1
		}
		defer clear(body)
	}
	out, err := (admin.ApplicationClient{Socket: layout.Socket, ExpectedUID: layout.UID}).Call(ctx, appvalues.AdminRequest{Action: "protection_" + action}, body)
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		fmt.Fprintln(stderr, fault.Describe(err).NextAction)
		return 1
	}
	var metadata map[string]any
	if json.Unmarshal(out, &metadata) != nil || metadata == nil {
		fmt.Fprintln(stderr, "invalid secret protection metadata")
		return 1
	}
	if json.NewEncoder(stdout).Encode(metadata) != nil {
		return 1
	}
	return 0
}

func runSecretRewrap(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("secret protection rewrap", flag.ContinueOnError)
	flags.SetOutput(stderr)
	layoutPath := flags.String("layout", "/etc/loki-go/secrets-admin.json", "installed private administrator channel layout")
	request := flags.String("request-id", "", "new UUID once; retain it and the same new passphrase for retries")
	resume := flags.Bool("resume", false, "resume a published change using only its original UUID and new passphrase")
	currentFile := flags.String("current-file", "", "owned mode-0600 original passphrase file")
	currentStdin := flags.Bool("current-stdin", false, "read the original passphrase from private stdin")
	newFile := flags.String("new-file", "", "owned mode-0600 target passphrase file")
	newStdin := flags.Bool("new-stdin", false, "read the target passphrase from private stdin")
	if flags.Parse(args) != nil || flags.NArg() != 0 || !controlpolicy.ApplicationUUID.MatchString(*request) ||
		*currentFile != "" && *currentStdin || *newFile != "" && *newStdin || *currentStdin && *newStdin || *resume && (*currentFile != "" || *currentStdin) {
		return 2
	}
	var layout applicationClientLayout
	if daemon.ReadJSON(*layoutPath, &layout) != nil || layout.UID == 0 {
		fmt.Fprintln(stderr, "installed secret protection channel is unavailable")
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var current []byte
	var err error
	if !*resume {
		fmt.Fprintln(stderr, "Original vault passphrase:")
		current, err = readProtectionInput(ctx, stdin, stderr, *currentFile, *currentStdin)
		if err != nil {
			fmt.Fprintln(stderr, "original passphrase input is unavailable or invalid")
			return 1
		}
		defer clear(current)
	}
	fmt.Fprintln(stderr, "New vault passphrase:")
	next, err := readProtectionInput(ctx, stdin, stderr, *newFile, *newStdin)
	if err != nil {
		fmt.Fprintln(stderr, "new passphrase input is unavailable or invalid")
		return 1
	}
	defer clear(next)
	if *newFile == "" && !*newStdin {
		fmt.Fprintln(stderr, "Confirm new vault passphrase:")
		repeat, err := readProtectionInput(ctx, stdin, stderr, "", false)
		matches := err == nil && bytes.Equal(next, repeat)
		clear(repeat)
		if !matches {
			fmt.Fprintln(stderr, "new passphrases do not match")
			return 1
		}
	}
	body, err := appvalues.ProtectionInput(current, next)
	if err != nil {
		fmt.Fprintln(stderr, "passphrases must be nonempty UTF-8 lines within the input bound")
		return 1
	}
	defer clear(body)
	action := "protection_rewrap"
	if *resume {
		action = "protection_rewrap_resume"
	}
	out, err := (admin.ApplicationClient{Socket: layout.Socket, ExpectedUID: layout.UID}).Call(ctx, appvalues.AdminRequest{Action: action, RequestID: *request}, body)
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		fmt.Fprintln(stderr, fault.Describe(err).NextAction)
		fmt.Fprintln(stderr, "Inspect protection status and retain the original request ID and target passphrase before retrying.")
		return 1
	}
	var metadata map[string]any
	if json.Unmarshal(out, &metadata) != nil || metadata == nil || json.NewEncoder(stdout).Encode(metadata) != nil {
		return 1
	}
	return 0
}

func readProtectionInput(ctx context.Context, stdin io.Reader, prompt io.Writer, path string, fromStdin bool) ([]byte, error) {
	if path == "" && !fromStdin {
		value, err := admin.ReadSecret(ctx, prompt)
		if err != nil || len(value) == 0 || len(value) > 65536 {
			return nil, unix.EINVAL
		}
		return []byte(value), nil
	}
	reader := stdin
	if path != "" {
		fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
		if err != nil {
			return nil, unix.EINVAL
		}
		f := os.NewFile(uintptr(fd), "private-passphrase-input")
		defer f.Close()
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
			return nil, unix.EINVAL
		}
		reader = f
	}
	body, err := io.ReadAll(io.LimitReader(reader, 65537))
	if err != nil || len(body) == 0 || len(body) > 65536 {
		clear(body)
		return nil, unix.EINVAL
	}
	return body, nil
}
