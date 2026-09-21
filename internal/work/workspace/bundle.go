package workspace

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"loki/internal/fault"
	"loki/internal/policy"
)

const MaxSharedBytes = 32 * 1024 * 1024

func (f *Files) Attachment(path string) ([]byte, error) {
	data, _, err := f.read(path, MaxSharedBytes)
	return data, err
}

type boundedZIP struct{ bytes.Buffer }

func (b *boundedZIP) Write(p []byte) (int, error) {
	if len(p) > MaxSharedBytes-b.Len() {
		return 0, fault.Error("compressed bundle exceeds the 32 MiB limit")
	}
	return b.Buffer.Write(p)
}

func (f *Files) Bundle(ctx context.Context, paths []string, filename string) ([]byte, map[string]any, error) {
	if len(paths) == 0 || len(paths) > 64 {
		return nil, nil, fault.Error("paths must contain between 1 and 64 entries")
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".zip") || filepath.Base(filename) != filename || strings.ContainsAny(filename, "\\\x00") {
		return nil, nil, fault.Error("bundle filename must be a plain .zip filename")
	}
	files := map[string][]byte{}
	total, excluded, visited := 0, 0, 0
	var walk func(string, int) error
	walk = func(path string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		visited++
		if depth > 128 || visited > 100000 {
			return fault.Error("bundle traversal exceeds limit")
		}
		file, err := f.Policy.Open(path, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		if !info.IsDir() {
			defer file.Close()
			if _, exists := files[path]; exists {
				return nil
			}
			if len(files) >= 512 {
				return fault.Error("bundle exceeds the 512 file limit")
			}
			if info.Size() > int64(MaxSharedBytes-total) {
				return fault.Error("bundle input exceeds the 32 MiB limit")
			}
			data, err := io.ReadAll(io.LimitReader(file, int64(MaxSharedBytes-total)+1))
			if err != nil {
				return err
			}
			if len(data) > MaxSharedBytes-total {
				return fault.Error("bundle input exceeds the 32 MiB limit")
			}
			files[path] = data
			total += len(data)
			return nil
		}
		defer file.Close()
		for {
			entries, readErr := file.ReadDir(128)
			for _, entry := range entries {
				next := filepath.Join(path, entry.Name())
				if _, err := policy.Relative(next); err != nil || entry.Type()&os.ModeSymlink != 0 {
					excluded++
					continue
				}
				if err := walk(next, depth+1); err != nil {
					return err
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	for _, path := range paths {
		rel, err := policy.Relative(path)
		if err != nil {
			return nil, nil, err
		}
		if err = walk(rel, 0); err != nil {
			return nil, nil, err
		}
	}
	if len(files) == 0 {
		return nil, nil, fault.Error("bundle contains no regular files")
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer boundedZIP
	archive := zip.NewWriter(&buffer)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		out, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return nil, nil, err
		}
		if _, err = out.Write(files[name]); err != nil {
			return nil, nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, nil, err
	}
	return buffer.Bytes(), map[string]any{"file_count": len(files), "input_bytes": total, "excluded_entries": excluded}, nil
}
