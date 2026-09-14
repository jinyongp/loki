// Package audit stores bounded non-secret runtime operation metadata.
package audit

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Log struct {
	Path string
	mu   sync.Mutex
}

const maxRecord = 16 * 1024
const maxTail = 1024 * 1024

func (l *Log) open(flags int) (*os.File, error) {
	fd, err := unix.Open(l.Path, flags|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "audit-log")
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Mode&0077 != 0 {
		f.Close()
		return nil, errors.New("audit log must be a private service-owned regular file")
	}
	return f, nil
}
func (l *Log) Append(record map[string]any) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maxRecord {
		return errors.New("audit record exceeds limit")
	}
	data = append(data, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.open(unix.O_WRONLY | unix.O_APPEND | unix.O_CREAT)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
func (l *Log) Runtime(operation string, uid uint32, success bool, profile *string) error {
	return l.Append(map[string]any{"timestamp": float64(time.Now().UnixMicro()) / 1e6, "operation": operation, "uid": uid, "success": success, "profile": profile})
}
func (l *Log) Read(limit int) (map[string]any, error) {
	limit = min(max(limit, 1), 200)
	records := []json.RawMessage{}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.open(unix.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{"records": records}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := max(int64(0), info.Size()-maxTail)
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxTail))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		_, data, _ = bytes.Cut(data, []byte{'\n'})
	}
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0 && len(records) < limit; i-- {
		line := lines[i]
		if len(line) <= maxRecord && json.Valid(line) {
			records = append(records, append(json.RawMessage(nil), line...))
		}
	}
	return map[string]any{"records": records}, nil
}
