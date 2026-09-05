package admin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"loki/internal/secret"
)

func TestDotenvStagingAndSourceOwnership(t *testing.T) {
	for _, change := range []string{"none", "replacement", "edit", "symlink"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "source.env")
			raw := []byte("TOKEN=synthetic-value\nOTHER=한글\n")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			d, err := OpenDotenv(path)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			inbox := filepath.Join(root, "inbox")
			result, err := d.Stage(inbox)
			if err != nil {
				t.Fatal(err)
			}
			staged := filepath.Join(inbox, result["import_id"].(string)+".env")
			info, err := os.Stat(staged)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("staged permissions %v %v", info, err)
			}
			controller := secret.Controller{InboxDirectory: inbox}
			imports, err := controller.ListImports()
			if err != nil || imports == nil {
				t.Fatalf("runtime inbox %v %v", imports, err)
			}
			switch change {
			case "replacement":
				os.Rename(path, path+".old")
				os.WriteFile(path, []byte("TOKEN=new\n"), 0600)
			case "edit":
				os.WriteFile(path, []byte("TOKEN=edited\n"), 0600)
			case "symlink":
				os.Rename(path, path+".old")
				os.Symlink(path+".old", path)
			}
			err = d.Remove()
			if change == "none" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("source remains")
				}
			} else {
				if err == nil {
					t.Fatal("changed source deleted")
				}
				if _, err = os.Lstat(path); err != nil {
					t.Fatal("changed source not restored")
				}
			}
		})
	}
}

func TestDotenvRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string][]byte{"empty": {}, "invalid": {255}, "large": []byte(strings.Repeat("x", secret.MaxInboxBytes+1)), "invalid-secret": []byte("not-assignment")} {
		path := filepath.Join(root, name)
		os.WriteFile(path, data, 0600)
		if source, err := OpenDotenv(path); err == nil {
			source.Close()
			t.Fatalf("accepted %s", name)
		}
	}
	path := filepath.Join(root, "valid")
	os.WriteFile(path, []byte("TOKEN=value"), 0600)
	os.Symlink(path, filepath.Join(root, "link"))
	unix.Mkfifo(filepath.Join(root, "fifo"), 0600)
	for _, name := range []string{"link", "fifo"} {
		if source, err := OpenDotenv(filepath.Join(root, name)); err == nil {
			source.Close()
			t.Fatalf("accepted %s", name)
		}
	}
}
