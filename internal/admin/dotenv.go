package admin

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/fault"
	"loki/internal/secret"
)

type Dotenv struct {
	Values map[string]string
	data   []byte
	parent *os.File
	name   string
	info   os.FileInfo
}

func OpenDotenv(path string) (*Dotenv, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	parent, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(path), &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	d := &Dotenv{parent: os.NewFile(uintptr(parent), "dotenv parent"), name: filepath.Base(path)}
	fd, err := unix.Openat(parent, d.name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		d.Close()
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "dotenv source")
	defer f.Close()
	d.info, err = f.Stat()
	if err == nil && (!d.info.Mode().IsRegular() || d.info.Size() < 1 || d.info.Size() > secret.MaxInboxBytes) {
		err = fault.Error("dotenv source must be a bounded nonempty regular file")
	}
	if err == nil {
		d.data, err = io.ReadAll(io.LimitReader(f, secret.MaxInboxBytes+1))
	}
	if err == nil && (len(d.data) > secret.MaxInboxBytes || !utf8.Valid(d.data)) {
		err = fault.Error("dotenv source is too large or not UTF-8")
	}
	if err == nil {
		d.Values, err = secret.ParseDotenv(string(d.data))
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

func (d *Dotenv) Close() error { return d.parent.Close() }

func randomName() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// Remove first takes ownership of a directory entry using a collision-free
// rename. A replaced or edited source is restored rather than deleted.
func (d *Dotenv) Remove() error {
	suffix, err := randomName()
	if err != nil {
		return err
	}
	private := ".loki-import-" + suffix
	parent := int(d.parent.Fd())
	if err = unix.Renameat2(parent, d.name, parent, private, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	restore := func() error {
		if err := unix.Renameat2(parent, private, parent, d.name, unix.RENAME_NOREPLACE); err != nil {
			return fault.Error("source changed; preserved file requires recovery from " + private)
		}
		return fault.Error("source changed after import; source was preserved")
	}
	fd, err := unix.Openat(parent, private, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return restore()
	}
	f := os.NewFile(uintptr(fd), "imported source")
	info, err := f.Stat()
	var data []byte
	if err == nil && info.Mode().IsRegular() && os.SameFile(info, d.info) {
		data, err = io.ReadAll(io.LimitReader(f, secret.MaxInboxBytes+1))
	}
	f.Close()
	if err != nil || !os.SameFile(info, d.info) || !bytes.Equal(data, d.data) {
		return restore()
	}
	if err = unix.Unlinkat(parent, private, 0); err != nil {
		return restore()
	}
	return d.parent.Sync()
}

func (d *Dotenv) Stage(inbox string) (map[string]any, error) {
	if !filepath.IsAbs(inbox) {
		return nil, fault.Error("secret inbox must be absolute")
	}
	if err := os.MkdirAll(inbox, 0700); err != nil {
		return nil, err
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, inbox, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	root := os.NewFile(uintptr(fd), "secret inbox")
	defer root.Close()
	info, err := root.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, fault.Error("secret inbox must be private and owned")
	}
	id, err := randomName()
	if err != nil {
		return nil, err
	}
	name := id + ".env"
	fileFD, err := unix.Openat(fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fileFD), "staged dotenv")
	_, err = file.Write(d.data)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Sync()
	}
	if err != nil {
		unix.Unlinkat(fd, name, 0)
		return nil, err
	}
	names := make([]string, 0, len(d.Values))
	for name := range d.Values {
		names = append(names, name)
	}
	sort.Strings(names)
	return map[string]any{"import_id": id, "secret_names": names, "count": len(names), "source_deleted": false}, nil
}

func (d *Dotenv) String() string { return fmt.Sprintf("dotenv source (%d names)", len(d.Values)) }
