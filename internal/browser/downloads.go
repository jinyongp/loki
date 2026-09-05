package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/cdp"
)

// Downloads are deliberately shared with the workspace group. Browser profile
// state continues to use the stricter private-directory policy.
func prepareDownloads(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return errors.New("browser downloads directory must not contain symlinks")
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Uid != uint32(os.Getuid()) || stat.Mode&0007 != 0 {
		return errors.New("browser downloads must be service-owned and inaccessible to other users")
	}
	return nil
}

var downloadID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type completedDownload struct{ guid, name string }
type downloads struct {
	mu        sync.Mutex
	names     map[string]string
	queue     chan completedDownload
	done      chan struct{}
	directory int
}

func newDownloads(path string) (*downloads, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	d := &downloads{names: map[string]string{}, queue: make(chan completedDownload, 128), done: make(chan struct{}), directory: fd}
	go func() {
		defer close(d.done)
		defer unix.Close(fd)
		for item := range d.queue {
			d.publish(item)
		}
	}()
	return d, nil
}
func (d *downloads) Close() { close(d.queue); <-d.done }
func downloadName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.ReplaceAll(name, "\x00", "")
	for len(name) > 200 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}
func (d *downloads) Event(e cdp.Event) {
	if e.Method != "Browser.downloadWillBegin" && e.Method != "Browser.downloadProgress" {
		return
	}
	var event struct{ GUID, SuggestedFilename, State string }
	if json.Unmarshal(e.Params, &event) != nil || !downloadID.MatchString(event.GUID) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if e.Method == "Browser.downloadWillBegin" {
		if len(d.names) < 128 {
			d.names[event.GUID] = downloadName(event.SuggestedFilename)
		}
		return
	}
	if event.State != "completed" && event.State != "canceled" {
		return
	}
	name, ok := d.names[event.GUID]
	delete(d.names, event.GUID)
	if event.State == "completed" && ok {
		// On saturation the already-safe GUID filename remains available.
		select {
		case d.queue <- completedDownload{event.GUID, name}:
		default:
		}
	}
}
func (d *downloads) publish(item completedDownload) {
	var stat unix.Stat_t
	if unix.Fstatat(d.directory, item.guid, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return
	}
	ext := filepath.Ext(item.name)
	base := strings.TrimSuffix(item.name, ext)
	for index := 0; index < 1000; index++ {
		name := item.name
		if index > 0 {
			name = fmt.Sprintf("%s (%d)%s", base, index, ext)
		}
		err := unix.Renameat2(d.directory, item.guid, d.directory, name, unix.RENAME_NOREPLACE)
		if err == nil {
			unix.Fsync(d.directory)
			return
		}
		if !errors.Is(err, unix.EEXIST) {
			return
		}
	}
}
