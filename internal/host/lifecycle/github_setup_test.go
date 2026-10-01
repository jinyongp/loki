package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubSetupStateRejectsSymlinksAndPublicPermissions(t *testing.T) {
	for _, variant := range []string{"directory-symlink", "public-directory", "file-symlink", "public-file"} {
		t.Run(variant, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			store, err := OpenFileStore(root)
			if err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			target := filepath.Join(outside, "outside.json")
			if err = os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "github-setup")
			if variant == "directory-symlink" {
				if err = os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
			} else {
				mode := os.FileMode(0700)
				if variant == "public-directory" {
					mode = 0755
				}
				if err = os.Mkdir(dir, mode); err != nil {
					t.Fatal(err)
				}
				if err = os.Chmod(dir, mode); err != nil {
					t.Fatal(err)
				}
				if variant == "file-symlink" {
					err = os.Symlink(target, filepath.Join(dir, "session.json"))
				}
				if variant == "public-file" {
					err = os.WriteFile(filepath.Join(dir, "session.json"), []byte("{}"), 0600)
					if err == nil {
						err = os.Chmod(filepath.Join(dir, "session.json"), 0644)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = store.ReadGitHubSetup(context.Background()); err == nil {
				t.Fatal("unsafe private setup state accepted")
			}
			err = store.WriteGitHubSetup(context.Background(), []byte("replacement"))
			if strings.Contains(variant, "directory") {
				if err == nil {
					t.Fatal("unsafe setup directory accepted for publication")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(filepath.Join(dir, "session.json"))
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
					t.Fatal("atomic publication did not replace unsafe file with private regular state")
				}
			}
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != "unchanged" {
				t.Fatal("outside file modified")
			}
		})
	}
}

func TestGitHubSetupStateUsesPrivateAtomicPublicationAndIndependentLock(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	lock, err := store.GitHubSetupLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if second, err := store.GitHubSetupLock(ctx); err == nil {
		second.Close()
		t.Fatal("concurrent setup lock accepted")
	}
	lifecycleLock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal("browser wait acquired lifecycle lock", err)
	}
	lifecycleLock.Close()
	if err = store.WriteGitHubSetup(ctx, []byte("private")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "github-setup", "session.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("state permissions invalid")
	}
	if err = store.ClearGitHubSetup(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := store.ReadGitHubSetup(ctx)
	if err != nil || len(raw) != 0 {
		t.Fatal("pending state retained")
	}
}
