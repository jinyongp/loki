package browser

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"loki/internal/cdp"
)

func TestChromiumSharedDownloads(t *testing.T) {
	d, address := chromeDriver(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download" || r.URL.Path == "/protected" {
			w.Header().Set("Content-Type", "text/plain")
			filename := "fixture.txt"
			if r.URL.Path == "/protected" {
				filename = "protected.txt"
			}
			w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
			fmt.Fprint(w, "download fixture")
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a name="download" href="/download">Download</a><a name="protected" href="/protected">Protected</a>`)
	}))
	if err := os.Mkdir(d.options.Downloads, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d.options.Downloads, os.ModeSetgid|0770); err != nil {
		t.Fatal(err)
	}
	callBrowser(t, d, "start", nil)
	callBrowser(t, d, "navigate", map[string]any{"url": address})
	clickDownload := func(name string) {
		t.Helper()
		state := callBrowser(t, d, "state", nil)
		callBrowser(t, d, "click", map[string]any{
			"index":                       elementIndex(t, state, name),
			"expected_browser_generation": state["browser_generation"],
			"expected_state_generation":   state["state_generation"],
		})
	}
	clickDownload("download")
	path := filepath.Join(d.options.Downloads, "fixture.txt")
	waitDownload := func(path string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(path); err == nil {
				if string(data) != "download fixture" {
					t.Fatal(string(data))
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		entries, _ := os.ReadDir(d.options.Downloads)
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatal("download was not saved", path, names)
	}
	waitDownload(path)
	victim := filepath.Join(d.options.Profile, "protected.txt")
	if err := os.WriteFile(victim, []byte("preserve profile file"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d.options.Downloads, "protected.txt")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatal(err)
	}
	clickDownload("protected")
	waitDownload(filepath.Join(d.options.Downloads, "protected (1).txt"))
	if data, err := os.ReadFile(victim); err != nil || string(data) != "preserve profile file" {
		t.Fatal("download overwrote symlink target", err)
	}
	if target, err := os.Readlink(link); err != nil || target != victim {
		t.Fatal("existing link was replaced", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		observed := callBrowser(t, d, "downloads", map[string]any{"since_sequence": 0, "limit": 500})
		items := observed["downloads"].([]map[string]any)
		finals := map[string]string{}
		for _, item := range items {
			if item["state"] == "completed" {
				if final, ok := item["final_filename"].(string); ok {
					finals[item["suggested_filename"].(string)] = final
				}
			}
		}
		if finals["fixture.txt"] == "fixture.txt" && finals["protected.txt"] == "protected (1).txt" {
			if observed["complete"] != true || observed["next_sequence"] == nil {
				t.Fatalf("download observation lacks completeness: %#v", observed)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("download observation did not publish final filenames")
}

func downloadEvent(t *testing.T, method string, payload map[string]any) cdp.Event {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return cdp.Event{Method: method, Params: raw}
}

func TestDownloadObservationTracksLatestStatusAndPublication(t *testing.T) {
	d := &downloads{records: map[string]*downloadRecord{}, queue: make(chan completedDownload, 4)}
	guid := "11111111-1111-1111-1111-111111111111"
	d.Event(downloadEvent(t, "Browser.downloadWillBegin", map[string]any{
		"guid": guid, "suggestedFilename": "../report.txt",
	}))
	d.Event(downloadEvent(t, "Browser.downloadProgress", map[string]any{
		"guid": guid, "state": "inProgress", "receivedBytes": 25, "totalBytes": 100,
	}))
	first := d.Observe(0, 500)
	items := first["downloads"].([]map[string]any)
	if len(items) != 1 {
		t.Fatalf("downloads = %#v", first)
	}
	item := items[0]
	if item["download_id"] != guid || item["state"] != "in_progress" ||
		item["suggested_filename"] != "report.txt" || item["final_filename"] != nil ||
		item["received_bytes"] != float64(25) || item["total_bytes"] != float64(100) {
		t.Fatalf("in-progress download = %#v", item)
	}
	if first["complete"] != true || first["next_sequence"] != int64(2) ||
		first["latest_sequence"] != int64(2) || first["oldest_sequence"] != int64(2) {
		t.Fatalf("initial observation = %#v", first)
	}

	d.Event(downloadEvent(t, "Browser.downloadProgress", map[string]any{
		"guid": guid, "state": "completed", "receivedBytes": 100, "totalBytes": 100,
	}))
	second := d.Observe(2, 500)
	items = second["downloads"].([]map[string]any)
	if len(items) != 1 || items[0]["state"] != "completed" || items[0]["sequence"] != int64(3) {
		t.Fatalf("completed observation = %#v", second)
	}
	d.recordPublished(guid, "report (1).txt")
	published := d.Observe(3, 500)
	items = published["downloads"].([]map[string]any)
	if len(items) != 1 || items[0]["final_filename"] != "report (1).txt" || items[0]["sequence"] != int64(4) {
		t.Fatalf("published observation = %#v", published)
	}

	canceledGUID := "22222222-2222-2222-2222-222222222222"
	d.Event(downloadEvent(t, "Browser.downloadWillBegin", map[string]any{
		"guid": canceledGUID, "suggestedFilename": "cancel.txt",
	}))
	d.Event(downloadEvent(t, "Browser.downloadProgress", map[string]any{
		"guid": canceledGUID, "state": "canceled", "receivedBytes": 10, "totalBytes": 100,
	}))
	canceled := d.Observe(4, 500)
	items = canceled["downloads"].([]map[string]any)
	if len(items) != 1 || items[0]["download_id"] != canceledGUID || items[0]["state"] != "canceled" {
		t.Fatalf("canceled observation = %#v", canceled)
	}
}

func TestDownloadObservationReportsTruncationAndRetentionLoss(t *testing.T) {
	d := &downloads{records: map[string]*downloadRecord{}, queue: make(chan completedDownload, 1)}
	for i := 0; i < 3; i++ {
		guid := fmt.Sprintf("00000000-0000-0000-0000-%012x", i+1)
		d.Event(downloadEvent(t, "Browser.downloadWillBegin", map[string]any{
			"guid": guid, "suggestedFilename": fmt.Sprintf("%d.txt", i),
		}))
		d.Event(downloadEvent(t, "Browser.downloadProgress", map[string]any{
			"guid": guid, "state": "canceled",
		}))
	}
	limited := d.Observe(0, 1)
	if limited["complete"] != false || limited["next_sequence"] != nil ||
		len(limited["downloads"].([]map[string]any)) != 1 {
		t.Fatalf("limited observation = %#v", limited)
	}
	full := d.Observe(0, 500)
	if full["complete"] != true || full["next_sequence"] != full["latest_sequence"] ||
		len(full["downloads"].([]map[string]any)) != 3 {
		t.Fatalf("full observation = %#v", full)
	}

	d = &downloads{records: map[string]*downloadRecord{}, queue: make(chan completedDownload, 1)}
	for i := 0; i < maxDownloadRecords; i++ {
		guid := fmt.Sprintf("10000000-0000-0000-0000-%012x", i+1)
		d.Event(downloadEvent(t, "Browser.downloadWillBegin", map[string]any{
			"guid": guid, "suggestedFilename": "terminal.txt",
		}))
		d.Event(downloadEvent(t, "Browser.downloadProgress", map[string]any{
			"guid": guid, "state": "canceled",
		}))
	}
	oldestSequence := d.records[d.order[0]].Sequence
	newGUID := "20000000-0000-0000-0000-000000000001"
	d.Event(downloadEvent(t, "Browser.downloadWillBegin", map[string]any{
		"guid": newGUID, "suggestedFilename": "new.txt",
	}))
	lost := d.Observe(0, 500)
	if lost["complete"] != false || lost["next_sequence"] != nil || d.droppedThrough < oldestSequence {
		t.Fatalf("retention loss = %#v droppedThrough=%d oldest=%d", lost, d.droppedThrough, oldestSequence)
	}
	recovered := d.Observe(d.droppedThrough, 500)
	if recovered["complete"] != true || recovered["next_sequence"] != recovered["latest_sequence"] {
		t.Fatalf("post-loss observation = %#v", recovered)
	}
}

func TestDownloadObservationMarksUntrackedProgressIncomplete(t *testing.T) {
	d := &downloads{records: map[string]*downloadRecord{}, queue: make(chan completedDownload, 1)}
	d.Event(downloadEvent(t, "Browser.downloadProgress", map[string]any{
		"guid":  "33333333-3333-3333-3333-333333333333",
		"state": "completed",
	}))
	result := d.Observe(0, 500)
	if result["complete"] != false || result["next_sequence"] != nil || d.droppedThrough != 1 {
		t.Fatalf("untracked progress observation = %#v dropped=%d", result, d.droppedThrough)
	}
}

func TestDownloadDirectoryBoundaries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "downloads")
	if err := prepareDownloads(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0777); err != nil {
		t.Fatal(err)
	}
	if prepareDownloads(path) == nil {
		t.Fatal("public downloads directory accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if prepareDownloads(link) == nil {
		t.Fatal("symlink downloads accepted")
	}
}

func TestDownloadPublicationPreservesExistingEntries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	d, err := newDownloads(root)
	if err != nil {
		t.Fatal(err)
	}
	for i, guid := range []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"} {
		if err := os.WriteFile(filepath.Join(root, guid), []byte(fmt.Sprint(i)), 0600); err != nil {
			t.Fatal(err)
		}
		begin, _ := json.Marshal(map[string]any{"guid": guid, "suggestedFilename": "../report.txt"})
		done, _ := json.Marshal(map[string]any{"guid": guid, "state": "completed"})
		d.Event(cdp.Event{Method: "Browser.downloadWillBegin", Params: begin})
		d.Event(cdp.Event{Method: "Browser.downloadProgress", Params: done})
	}
	d.Close()
	for name, want := range map[string]string{"report.txt": "existing", "report (1).txt": "0", "report (2).txt": "1"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatal(name, string(data), err)
		}
	}
	for _, name := range []string{"../escape.txt", `C:\folder\file.txt`, "..", "\x00", strings.Repeat("😀", 100)} {
		safe := downloadName(name)
		if strings.ContainsAny(safe, "/\\\x00") || safe == ".." || safe == "" || len(safe) > 200 || !utf8.ValidString(safe) {
			t.Fatal(name, safe)
		}
	}
}
