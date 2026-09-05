package action

import (
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/policy"
)

var installedVersion = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)

// Read only bounded regular metadata beneath the pinned workspace. Refuse
// symlinks and non-regular files so a version hint cannot open private host data
// or block the privileged controller on a FIFO/device.
func versionHint(workspace *os.File, name string) (string, error) {
	fd, err := unix.Openat2(int(workspace.Fd()), name, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NONBLOCK, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), "action Node version hint")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", err
	}
	if len(data) > 4096 {
		return "", fault.Error("action Node version file is too large")
	}
	if !utf8.Valid(data) {
		return "", fault.Error("action Node version file is not valid UTF-8")
	}
	value := strings.TrimSpace(string(data))
	if value == "" || utf8.RuneCountInString(value) > 128 || strings.ContainsAny(value, "\x00/\\") {
		return "", nil
	}
	return value, nil
}

func nodeVersion(workspace *os.File, cwd string) (string, error) {
	for _, name := range []string{".node-version", ".nvmrc"} {
		version, err := versionHint(workspace, filepath.Join(cwd, name))
		if err != nil || version != "" {
			return version, err
		}
	}
	fd, err := unix.Openat2(int(workspace.Fd()), ".loki/fnm/node-versions", &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
			return "", fault.Error("no FNM-managed Node version is installed")
		}
		return "", err
	}
	directory := os.NewFile(uintptr(fd), "action Node installations")
	defer directory.Close()
	var best string
	var numbers [3]*big.Int
	for {
		entries, err := directory.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		for _, entry := range entries {
			match := installedVersion.FindStringSubmatch(entry.Name())
			if !entry.IsDir() || match == nil {
				continue
			}
			var current [3]*big.Int
			comparison := 0
			for i := range current {
				current[i], _ = new(big.Int).SetString(match[i+1], 10)
				if best != "" && comparison == 0 {
					comparison = current[i].Cmp(numbers[i])
				}
			}
			if best == "" || comparison > 0 || comparison == 0 && entry.Name() > best {
				best, numbers = entry.Name(), current
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if best == "" {
		return "", fault.Error("no FNM-managed Node version is installed")
	}
	return best, nil
}

func resolvedCommand(command []string, workspace *os.File, cwd string) ([]string, error) {
	if len(command) == 0 {
		return nil, errors.New("missing action command")
	}
	if err := policy.ValidateExec(command[0], command[1:]); err != nil {
		return nil, err
	}
	executable, exists := policy.ExecutablePath(command[0])
	if !exists {
		return nil, fault.Error("action executable is not allowed")
	}
	if !slices.Contains([]string{"node", "npm", "pnpm", "just", "actions-up"}, command[0]) {
		return append([]string{executable}, command[1:]...), nil
	}
	version, err := nodeVersion(workspace, cwd)
	if err != nil {
		return nil, err
	}
	argv := []string{"/home/linuxbrew/.linuxbrew/bin/fnm", "exec", "--using", version}
	if command[0] == "pnpm" {
		argv = append(argv, "corepack", "pnpm")
	} else {
		argv = append(argv, executable)
	}
	return append(argv, command[1:]...), nil
}
