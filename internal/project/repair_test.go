package project

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRootTaskMetadataRepair(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_ROOT_METADATA_TESTS") == "1" {
			t.Fatal("root metadata acceptance requires root")
		}
		t.Skip("root metadata acceptance")
	}
	root := t.TempDir()
	project := filepath.Join(root, strings.Repeat("a", 32))
	taskroot := filepath.Join(project, "taskwarrior")
	data := filepath.Join(taskroot, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(data, "taskchampion.sqlite3")
	os.WriteFile(file, []byte("database contents"), 0600)
	os.Chown(data, 65534, 65534)
	os.Chown(file, 65534, 65534)
	os.WriteFile(filepath.Join(taskroot, "taskrc"), []byte("hooks=0\n"), 0600)
	for range 2 {
		count, err := RepairMetadata(root, 65534)
		if err != nil || count != 1 {
			t.Fatal(count, err)
		}
	}
	for _, path := range []string{project, taskroot, filepath.Join(taskroot, "taskrc")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat := info.Sys().(*syscall.Stat_t)
		mode := os.FileMode(0710)
		if path == filepath.Join(taskroot, "taskrc") {
			mode = 0640
		}
		if stat.Uid != 0 || stat.Gid != 65534 || info.Mode().Perm() != mode {
			t.Fatalf("metadata %s %+v", path, stat)
		}
	}
	info, _ := os.Stat(file)
	raw, _ := os.ReadFile(file)
	if string(raw) != "database contents" || info.Sys().(*syscall.Stat_t).Uid != 65534 || info.Mode().Perm() != 0600 {
		t.Fatal("database changed")
	}
	os.Rename(filepath.Join(taskroot, "taskrc"), filepath.Join(taskroot, "original"))
	os.Symlink(file, filepath.Join(taskroot, "taskrc"))
	if _, err := RepairMetadata(root, 0); err == nil {
		t.Fatal("symlink metadata accepted")
	}
	info, _ = os.Stat(project)
	if info.Sys().(*syscall.Stat_t).Gid != 65534 {
		t.Fatal("invalid project partially changed")
	}
}
