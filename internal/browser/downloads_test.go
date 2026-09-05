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
	state := callBrowser(t, d, "state", nil)
	callBrowser(t, d, "click", map[string]any{"index": elementIndex(t, state, "download")})
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
	callBrowser(t, d, "click", map[string]any{"index": elementIndex(t, state, "protected")})
	waitDownload(filepath.Join(d.options.Downloads, "protected (1).txt"))
	if data, err := os.ReadFile(victim); err != nil || string(data) != "preserve profile file" {
		t.Fatal("download overwrote symlink target", err)
	}
	if target, err := os.Readlink(link); err != nil || target != victim {
		t.Fatal("existing link was replaced", err)
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
