package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"syscall"

	"github.com/pelletier/go-toml/v2/unstable"
	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/state"
)

type editSpan struct{ start, end int }

func keys(node *unstable.Node) []string {
	result := []string{}
	it := node.Key()
	for it.Next() {
		result = append(result, string(it.Node().Data))
	}
	return result
}

func RenderProcessLimit(source []byte, name string, value int) ([]byte, error) {
	key := map[string]string{"max-action-processes": "max_action_processes", "max-action-processes-per-profile": "max_action_processes_per_profile"}[name]
	if key == "" || value < 1 || value > 32 {
		return nil, fault.Error("action process setting must be known and between 1 and 32")
	}
	if _, err := Parse(source); err != nil {
		return nil, err
	}
	var parser unstable.Parser
	parser.Reset(source)
	root := true
	span := editSpan{-1, -1}
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			root = false
		}
		if root && node.Kind == unstable.KeyValue && slices.Equal(keys(node), []string{key}) {
			r := node.Value().Raw
			span = editSpan{int(r.Offset), int(r.Offset + r.Length)}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	var result []byte
	if span.start < 0 {
		result = append([]byte(key+" = "+strconv.Itoa(value)+"\n"), source...)
	} else {
		result = append(result, source[:span.start]...)
		result = append(result, strconv.Itoa(value)...)
		result = append(result, source[span.end:]...)
	}
	if _, err := Parse(result); err != nil {
		return nil, err
	}
	return result, nil
}

func RenderExecutables(source []byte, values map[string]string) ([]byte, error) {
	if _, err := Parse(source); err != nil {
		return nil, err
	}
	spans := []editSpan{}
	table := []string{}
	var parser unstable.Parser
	parser.Reset(source)
	for parser.NextExpression() {
		node := parser.Expression()
		if node.Kind == unstable.Table || node.Kind == unstable.ArrayTable {
			table = keys(node)
			if slices.Equal(table, []string{"executables"}) {
				it := node.Key()
				it.Next()
				offset := int(it.Node().Raw.Offset)
				start := bytes.LastIndexByte(source[:offset], '\n') + 1
				end := bytes.IndexByte(source[offset:], '\n')
				if end < 0 {
					end = len(source)
				} else {
					end += offset + 1
				}
				spans = append(spans, editSpan{start, end})
			}
		} else if node.Kind == unstable.KeyValue {
			path := append(slices.Clone(table), keys(node)...)
			if len(path) > 0 && path[0] == "executables" {
				r := node.Raw
				spans = append(spans, editSpan{int(r.Offset), int(r.Offset + r.Length)})
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, err
	}
	var result []byte
	offset := 0
	for _, span := range spans {
		result = append(result, source[offset:span.start]...)
		offset = span.end
	}
	result = append(result, source[offset:]...)
	result = append(result, []byte("\n[executables]\n")...)
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !NamePattern.MatchString(name) || !filepath.IsAbs(values[name]) {
			return nil, fault.Error("invalid executable override")
		}
		result = append(result, []byte(strconv.Quote(name)+" = "+strconv.Quote(values[name])+"\n")...)
	}
	if _, err := Parse(result); err != nil {
		return nil, err
	}
	return result, nil
}

// Edit serializes administrative changes and preserves file ownership/mode.
// The containing directory must be controlled by this user or root.
func Edit(ctx context.Context, path string, change func([]byte) ([]byte, error)) (Config, error) {
	if !filepath.IsAbs(path) {
		return Config{}, fault.Error("configuration path must be absolute")
	}
	directory := filepath.Dir(path)
	fd, err := unix.Openat2(unix.AT_FDCWD, directory, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return Config{}, err
	}
	parent := os.NewFile(uintptr(fd), "configuration parent")
	defer parent.Close()
	info, err := parent.Stat()
	if err != nil {
		return Config{}, err
	}
	owner := info.Sys().(*syscall.Stat_t).Uid
	if info.Mode().Perm()&0022 != 0 || owner != 0 && owner != uint32(os.Geteuid()) {
		return Config{}, fault.Error("configuration directory must be protected")
	}
	release, err := state.LockFile(ctx, filepath.Join(directory, ".config.lock"))
	if err != nil {
		return Config{}, err
	}
	defer release()
	fileFD, err := unix.Openat(fd, filepath.Base(path), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return Config{}, err
	}
	file := os.NewFile(uintptr(fileFD), "configuration")
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return Config{}, err
	}
	metadata := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Size() > 1048576 || info.Mode().Perm()&0022 != 0 || metadata.Uid != 0 && metadata.Uid != uint32(os.Geteuid()) {
		return Config{}, fault.Error("configuration must be a protected regular file")
	}
	source, err := io.ReadAll(io.LimitReader(file, 1048577))
	if err != nil {
		return Config{}, err
	}
	if len(source) > 1048576 {
		return Config{}, fault.Error("configuration is too large")
	}
	updated, err := change(source)
	if err != nil {
		return Config{}, err
	}
	parsed, err := Parse(updated)
	if err != nil {
		return Config{}, err
	}
	if bytes.Equal(source, updated) {
		return parsed, nil
	}
	temporary, err := os.CreateTemp(directory, ".config-")
	if err != nil {
		return Config{}, err
	}
	defer os.Remove(temporary.Name())
	_, err = temporary.Write(updated)
	if err == nil {
		err = temporary.Chown(int(metadata.Uid), int(metadata.Gid))
	}
	if err == nil {
		err = temporary.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Config{}, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return Config{}, err
	}
	if !os.SameFile(info, current) || !info.ModTime().Equal(current.ModTime()) || info.Size() != current.Size() {
		return Config{}, errors.New("configuration changed concurrently")
	}
	if err = os.Rename(temporary.Name(), path); err != nil {
		return Config{}, err
	}
	if err = parent.Sync(); err != nil {
		return Config{}, err
	}
	return parsed, nil
}
