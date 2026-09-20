package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/state"
)

const MaxInboxBytes = 2_000_000

var dotenvNewline = regexp.MustCompile(`\r\n|[\n\r\v\f\x1c-\x1e\x{0085}\x{2028}\x{2029}]`)

func trim(value string) string {
	return strings.TrimFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f })
}

func ParseDotenv(source string) (map[string]string, error) {
	values := map[string]string{}
	for i, raw := range dotenvNewline.Split(source, -1) {
		line := trim(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = trim(line[7:])
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fault.Error(fmt.Sprintf("invalid dotenv assignment at line %d", i+1))
		}
		name, value = trim(name), trim(value)
		if err := SecretName(name); err != nil {
			return nil, err
		}
		if len(value) >= 2 && value[0] == value[len(value)-1] && (value[0] == '\'' || value[0] == '"') {
			if value[0] == '"' {
				var decoded string
				if json.Unmarshal([]byte(value), &decoded) != nil {
					return nil, fault.Error(fmt.Sprintf("invalid quoted dotenv value at line %d", i+1))
				}
				value = decoded
			} else {
				value = value[1 : len(value)-1]
			}
		}
		values[name] = value
	}
	if len(values) == 0 {
		return nil, fault.Error("dotenv import must contain secrets")
	}
	return values, nil
}
func (c Controller) inbox() string {
	if c.InboxDirectory != "" {
		return c.InboxDirectory
	}
	return filepath.Join(c.StateDirectory, "inbox")
}
func (c Controller) checkInbox() error {
	root := c.inbox()
	if !filepath.IsAbs(root) {
		return fault.Error("secret inbox must be an absolute private directory")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	if resolved != filepath.Clean(root) {
		return fault.Error("secret inbox path is unsafe")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return fault.Error("secret inbox must be a private owned directory")
	}
	return nil
}
func (c Controller) ListImportsPage(offset, limit int) (map[string]any, error) {
	items := []map[string]any{}
	if err := c.checkInbox(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_, _, pageLimit, _, _, pageErr := pageRange(0, offset, limit, 200)
			if pageErr != nil {
				return nil, pageErr
			}
			return map[string]any{
				"imports": items, "offset": 0, "limit": pageLimit, "has_more": false,
				"next_offset": nil, "total": 0, "complete": true,
			}, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(c.inbox())
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".env") || !idPattern.MatchString(strings.TrimSuffix(name, ".env")) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		items = append(items, map[string]any{"import_id": strings.TrimSuffix(name, ".env"), "bytes": info.Size(), "created_at": float64(info.ModTime().UnixNano()) / 1e9})
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i]["created_at"].(float64), items[j]["created_at"].(float64)
		if left == right {
			return items[i]["import_id"].(string) < items[j]["import_id"].(string)
		}
		return left < right
	})
	start, end, pageLimit, hasMore, nextOffset, err := pageRange(len(items), offset, limit, 200)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"imports": items[start:end], "offset": start, "limit": pageLimit,
		"has_more": hasMore, "next_offset": nextOffset, "total": len(items), "complete": true,
	}, nil
}

func (c Controller) ListImports() (map[string]any, error) {
	return c.ListImportsPage(0, 200)
}
func (c Controller) ImportStaged(ctx context.Context, name, id string) (map[string]any, error) {
	if err := applicationProfileName(name); err != nil {
		return nil, err
	}
	if !idPattern.MatchString(id) {
		return nil, fault.Error("invalid secret import ID")
	}
	if err := c.checkInbox(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fault.Error("unknown secret import")
		}
		return nil, err
	}
	release, err := state.LockFile(ctx, filepath.Join(c.inbox(), "imports.lock"))
	if err != nil {
		return nil, err
	}
	defer release()
	path := filepath.Join(c.inbox(), id+".env")
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fault.Error("unknown secret import")
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) || info.Size() < 1 || info.Size() > MaxInboxBytes {
		return nil, fault.Error("staged dotenv file has unsafe ownership or permissions")
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxInboxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxInboxBytes {
		return nil, fault.Error("staged dotenv file is too large")
	}
	if !utf8.Valid(raw) {
		return nil, fault.Error("staged dotenv file must be UTF-8")
	}
	values, err := ParseDotenv(string(raw))
	if err != nil {
		return nil, err
	}
	result, err := c.ImportValues(ctx, name, values)
	if err != nil {
		return nil, err
	}
	// Only consume the same inode that was read, after its state commit succeeds.
	current, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, current) {
		return nil, fault.Error("staged dotenv source changed during import")
	}
	if err = os.Remove(path); err != nil {
		return nil, err
	}
	dir, err := os.Open(c.inbox())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if err = dir.Sync(); err != nil {
		return nil, err
	}
	result["import_id"] = id
	result["source_deleted"] = true
	return result, nil
}
