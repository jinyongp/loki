package browser

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
	"loki/internal/integrations/browser/internal/cdp"
)

const maxDownloadRecords = 128

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

var downloadID = regexp.MustCompile("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")

type completedDownload struct{ guid, name string }

type downloadRecord struct {
	GUID              string
	State             string
	SuggestedFilename string
	FinalFilename     string
	ReceivedBytes     float64
	TotalBytes        float64
	Sequence          int64
	UpdatedAt         string
}

func (r downloadRecord) public() map[string]any {
	final := any(nil)
	if r.FinalFilename != "" {
		final = r.FinalFilename
	}
	return map[string]any{
		"download_id":        r.GUID,
		"state":              r.State,
		"suggested_filename": r.SuggestedFilename,
		"final_filename":     final,
		"received_bytes":     r.ReceivedBytes,
		"total_bytes":        r.TotalBytes,
		"sequence":           r.Sequence,
		"updated_at":         r.UpdatedAt,
	}
}

type downloads struct {
	mu             sync.Mutex
	records        map[string]*downloadRecord
	order          []string
	sequence       int64
	droppedThrough int64
	queue          chan completedDownload
	done           chan struct{}
	directory      int
}

func newDownloads(path string) (*downloads, error) {
	fd, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS})
	if err != nil {
		return nil, err
	}
	d := &downloads{
		records:   map[string]*downloadRecord{},
		queue:     make(chan completedDownload, maxDownloadRecords),
		done:      make(chan struct{}),
		directory: fd,
	}
	go func() {
		defer close(d.done)
		defer unix.Close(fd)
		for item := range d.queue {
			if final := d.publish(item); final != "" {
				d.recordPublished(item.guid, final)
			}
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

func downloadState(state string) (string, bool) {
	switch state {
	case "", "inProgress":
		return "in_progress", true
	case "completed":
		return "completed", true
	case "canceled":
		return "canceled", true
	default:
		return "", false
	}
}

func downloadUpdatedAt() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000+00:00")
}

func (d *downloads) nextSequence() int64 {
	d.sequence++
	return d.sequence
}

func (d *downloads) makeRoom() bool {
	if len(d.records) < maxDownloadRecords {
		return true
	}
	for index, guid := range d.order {
		record := d.records[guid]
		if record == nil || record.State == "in_progress" {
			continue
		}
		if record.Sequence > d.droppedThrough {
			d.droppedThrough = record.Sequence
		}
		delete(d.records, guid)
		d.order = append(d.order[:index], d.order[index+1:]...)
		return true
	}
	return false
}

func (d *downloads) Event(e cdp.Event) {
	if e.Method != "Browser.downloadWillBegin" && e.Method != "Browser.downloadProgress" {
		return
	}
	var event struct {
		GUID              string
		SuggestedFilename string
		State             string
		ReceivedBytes     float64
		TotalBytes        float64
	}
	if json.Unmarshal(e.Params, &event) != nil || !downloadID.MatchString(event.GUID) {
		return
	}
	state, validState := downloadState(event.State)
	if e.Method == "Browser.downloadProgress" && !validState {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	sequence := d.nextSequence()

	if e.Method == "Browser.downloadWillBegin" {
		record := d.records[event.GUID]
		if record == nil {
			if !d.makeRoom() {
				if sequence > d.droppedThrough {
					d.droppedThrough = sequence
				}
				return
			}
			record = &downloadRecord{GUID: event.GUID}
			d.records[event.GUID] = record
			d.order = append(d.order, event.GUID)
		}
		record.State = "in_progress"
		record.SuggestedFilename = downloadName(event.SuggestedFilename)
		record.Sequence = sequence
		record.UpdatedAt = downloadUpdatedAt()
		return
	}

	record := d.records[event.GUID]
	if record == nil {
		if sequence > d.droppedThrough {
			d.droppedThrough = sequence
		}
		return
	}
	previousState := record.State
	record.State = state
	if event.ReceivedBytes >= 0 {
		record.ReceivedBytes = event.ReceivedBytes
	}
	if event.TotalBytes >= 0 {
		record.TotalBytes = event.TotalBytes
	}
	record.Sequence = sequence
	record.UpdatedAt = downloadUpdatedAt()
	if state == "completed" && previousState != "completed" {
		select {
		case d.queue <- completedDownload{guid: event.GUID, name: record.SuggestedFilename}:
		default:
			// A saturated publication queue leaves the safe GUID file in place.
		}
	}
}

func (d *downloads) recordPublished(guid, final string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	record := d.records[guid]
	if record == nil || record.State != "completed" {
		return
	}
	record.FinalFilename = final
	record.Sequence = d.nextSequence()
	record.UpdatedAt = downloadUpdatedAt()
}

func (d *downloads) Observe(since int64, limit int) map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	limit = max(1, min(limit, 500))
	records := make([]downloadRecord, 0, len(d.records))
	oldest := int64(0)
	for _, guid := range d.order {
		record := d.records[guid]
		if record == nil {
			continue
		}
		if oldest == 0 || record.Sequence < oldest {
			oldest = record.Sequence
		}
		if record.Sequence > since {
			records = append(records, *record)
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Sequence == records[j].Sequence {
			return records[i].GUID < records[j].GUID
		}
		return records[i].Sequence < records[j].Sequence
	})
	available := len(records)
	if len(records) > limit {
		records = records[len(records)-limit:]
	}
	items := make([]map[string]any, 0, len(records))
	for _, record := range records {
		items = append(items, record.public())
	}
	complete := d.droppedThrough <= since && available <= limit
	return map[string]any{
		"downloads":       items,
		"latest_sequence": d.sequence,
		"oldest_sequence": oldest,
		"next_sequence":   observationCursor(d.sequence, complete),
		"retained":        len(d.records),
		"complete":        complete,
	}
}

func (d *downloads) publish(item completedDownload) string {
	var stat unix.Stat_t
	if unix.Fstatat(d.directory, item.guid, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return ""
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
			_ = unix.Fsync(d.directory)
			return name
		}
		if !errors.Is(err, unix.EEXIST) {
			return ""
		}
	}
	return ""
}
