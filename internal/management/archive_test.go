package management

import (
	"archive/zip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestArchiveRejectsTraversalAndLinks(t *testing.T) {
	for _, name := range []string{"../outside", "/outside", "a/../../outside", "a\\outside", "C:/outside"} {
		if _, err := archivePath(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "tool.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	header := &zip.FileHeader{Name: "link"}
	header.SetMode(os.ModeSymlink | 0777)
	w, err := z.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("../outside"))
	z.Close()
	f.Close()
	if err := extract(archive, "zip", dir); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestBrowserFrameworkAliases(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("macOS framework extraction uses Unix symlinks")
	}
	base := "chrome/Chrome.app/Contents/Frameworks/X.framework/"
	archive := frameworkArchive(t, []frameworkEntry{
		{base + "Versions/A/X", "signed payload", false},
		{base + "Versions/Current", "A", true},
		{base + "X", "Versions/Current/X", true},
	})
	destination := t.TempDir()
	if err := extractWithBrowserLinks(archive, "zip", destination, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(base+"X")))
	if err != nil || string(data) != "signed payload" {
		t.Fatalf("framework alias lost its file: %q %v", data, err)
	}
	if target, err := os.Readlink(filepath.Join(destination, filepath.FromSlash(base+"Versions/Current"))); err != nil || target != "A" {
		t.Fatalf("vendor link spelling changed: %q %v", target, err)
	}
	if err := extract(archive, "zip", t.TempDir()); err == nil {
		t.Fatal("generic archive extraction accepted framework aliases")
	}
	for _, entries := range [][]frameworkEntry{
		{{base + "escape", "../../../../../outside", true}},
		{{base + "dangling", "missing", true}},
		{{base + "a", "b", true}, {base + "b", "a", true}},
		{{base + "alias", "actual", true}, {base + "alias/file", "hidden write", false}, {base + "actual/file", "safe", false}},
		{{base + "alias/file", "hidden write", false}, {base + "alias", "actual", true}, {base + "actual/file", "safe", false}},
		{{"node/link", "../chrome/Chrome.app/Contents", true}},
	} {
		if err := extractWithBrowserLinks(frameworkArchive(t, entries), "zip", t.TempDir(), true); err == nil {
			t.Fatalf("accepted unsafe framework graph: %+v", entries)
		}
	}
}

type frameworkEntry struct {
	name, contents string
	link           bool
}

func frameworkArchive(t *testing.T, entries []frameworkEntry) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "browser.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	packed := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name}
		if entry.link {
			header.SetMode(os.ModeSymlink | 0777)
		} else {
			header.SetMode(0644)
		}
		writer, err := packed.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := packed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestMutationLockSerializesAndReleases(t *testing.T) {
	s := Store{Root: t.TempDir()}
	release, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	if other, err := s.Lock(); err == nil {
		other()
		release()
		t.Fatal("second writer acquired lock")
	}
	release()
	other, err := s.Lock()
	if err != nil {
		t.Fatal(err)
	}
	other()
}
