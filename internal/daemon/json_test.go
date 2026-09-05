package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProtectedServiceLayout(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "layout.json")
	var result struct{ Path string }
	for _, data := range []string{`{"Path":"/fixture"}`, `{"Unknown":1}`, `{} {}`, `null`, strings.Repeat(" ", 1048577)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		err := ReadJSON(path, &result)
		if data == `{"Path":"/fixture"}` {
			if err != nil || result.Path != "/fixture" {
				t.Fatal(result, err)
			}
		} else if err == nil {
			t.Fatal("invalid layout accepted")
		}
	}
	if err := os.WriteFile(path, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	if ReadJSON(path, &result) == nil {
		t.Fatal("writable layout accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if ReadJSON(link, &result) == nil {
		t.Fatal("symlink layout accepted")
	}
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if ReadJSON(fifo, &result) == nil {
		t.Fatal("special file accepted")
	}
}
