package action

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/secret"
)

const sandboxSnapshots = "/var/lib/loki/local-deploy/snapshots"

type materialization struct {
	once                      sync.Once
	result                    error
	cleared                   bool
	layout                    Layout
	target                    string
	workspace, binary, marker *os.File
}

func duplicate(file *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "owned action resource"), nil
}

func (m *materialization) close() error {
	m.once.Do(func() {
		_, m.result = runnerFileOperation(context.Background(), m.layout, m.workspace, m.binary, m.marker, "clear", m.target)
		m.marker.Close() // Releasing the lease permits safe recovery on the next run.
		m.workspace.Close()
		m.binary.Close()
	})
	return m.result
}

func beginMaterialization(layout Layout, plan secret.ActionPlan, workspace, binary *os.File) (*materialization, error) {
	return materializationOperation(layout, plan, workspace, binary, "claim")
}

func materializationOperation(layout Layout, plan secret.ActionPlan, workspace, binary *os.File, operation string) (*materialization, error) {
	if err := validateMaterializationPaths(layout); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(layout.MaterializationDirectory) {
		return nil, errors.New("materialization directory is not configured")
	}
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return nil, err
	}
	relative := plan.Policy.MaterializeEnvPath
	if relative == "" {
		relative = ".tmp/.loki-action-session-" + hex.EncodeToString(suffix[:]) + ".env"
	}
	if !relativeFile(relative) {
		return nil, fault.Error("materialized env path must be workspace-relative")
	}
	target := path.Join(strings.TrimPrefix(plan.VisibleCWD, "/workspace"), relative)
	target = strings.TrimPrefix(target, "/")
	if !relativeFile(target) {
		return nil, fault.Error("materialized env path must be workspace-relative")
	}
	if err := os.MkdirAll(layout.MaterializationDirectory, 0700); err != nil {
		return nil, err
	}
	dirFD, err := unix.Open(layout.MaterializationDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(dirFD), "private materialization directory")
	defer dir.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &stat); err != nil || stat.Mode&0777 != 0700 || stat.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("materialization directory must be private and service-owned")
	}
	var marker *os.File
	slots := 1
	if plan.Policy.MaterializeEnvPath == "" {
		slots = 32
	}
	for slot := range slots {
		key := filepath.Join(layout.Workspace, filepath.FromSlash(target))
		if plan.Policy.MaterializeEnvPath == "" {
			key = filepath.Join(layout.Workspace, path.Dir(target)) + "\x00session-slot\x00" + strconv.Itoa(slot)
		}
		digest := sha256.Sum256([]byte(key))
		fd, err := unix.Openat(int(dir.Fd()), hex.EncodeToString(digest[:])+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0600)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), "materialization lease")
		if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
			file.Close()
			return nil, errors.New("materialization lease must be a private regular file")
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			marker = file
			break
		}
		file.Close()
		if !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, err
		}
	}
	if marker == nil {
		return nil, fault.Error("materialized secret target is in use")
	}
	ok := false
	defer func() {
		if !ok {
			marker.Close()
		}
	}()
	if err := dir.Sync(); err != nil {
		return nil, err
	}
	if plan.Policy.MaterializeEnvPath == "" {
		record, err := readMaterializationRecord(marker)
		if err != nil {
			return nil, err
		}
		if record != nil {
			base := path.Base(record.Target)
			suffix := strings.TrimSuffix(strings.TrimPrefix(base, ".loki-action-session-"), ".env")
			if path.Dir(record.Target) != path.Dir(target) || base != ".loki-action-session-"+suffix+".env" || !launchTokenPattern.MatchString(suffix) {
				return nil, fault.Error("materialized secret marker target mismatch")
			}
			if _, err := runnerFileOperation(context.Background(), layout, workspace, binary, marker, "clear", record.Target); err != nil {
				return nil, err
			}
		}
	}
	workspaceCopy, err := duplicate(workspace)
	if err != nil {
		return nil, err
	}
	binaryCopy, err := duplicate(binary)
	if err != nil {
		workspaceCopy.Close()
		return nil, err
	}
	m := &materialization{layout: layout, target: target, workspace: workspaceCopy, binary: binaryCopy, marker: marker}
	result, err := runnerFileOperation(context.Background(), layout, workspace, binary, marker, operation, target)
	if err != nil {
		workspaceCopy.Close()
		binaryCopy.Close()
		return nil, err
	}
	m.cleared = result.Cleared
	ok = true
	return m, nil
}

func dotenvValue(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n#'\"") {
		return value
	}
	value = strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\r", "\\r", "\n", "\\n", "\t", "\\t").Replace(value)
	return "\"" + value + "\""
}

func materializedEnvironment(plan secret.ActionPlan, environment []string) []byte {
	values := map[string]string{}
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	names := plan.SelectedNames()
	sort.Strings(names)
	var data strings.Builder
	for _, name := range names {
		data.WriteString(name)
		data.WriteByte('=')
		data.WriteString(dotenvValue(values[name]))
		data.WriteByte('\n')
	}
	return []byte(data.String())
}
