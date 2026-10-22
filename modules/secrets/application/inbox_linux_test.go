//go:build linux

package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInboxResetExpiresOwnedInputAndPreservesUnknownState(t *testing.T) {
	for _, kind := range []string{"owned", "unknown", "symlink", "public", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			inbox, err := os.MkdirTemp("/dev/shm", "loki-inbox-reset-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(inbox)
			path := filepath.Join(inbox, strings.Repeat("a", 32)+".data")
			if err := os.WriteFile(path, []byte("synthetic-private-pending-input"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "unknown":
				err = os.WriteFile(filepath.Join(inbox, "unowned"), []byte("preserve"), 0600)
			case "symlink":
				err = os.Symlink(path, filepath.Join(inbox, strings.Repeat("b", 32)+".data"))
			case "public":
				err = os.Chmod(path, 0644)
			case "hardlink":
				err = os.Link(path, filepath.Join(inbox, strings.Repeat("b", 32)+".data"))
			}
			if err != nil {
				t.Fatal(err)
			}
			err = ResetInbox(inbox)
			if kind == "owned" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("pending input survived new instance", err)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe inbox adopted")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("rejected reset removed state", err)
				}
			}
		})
	}
}
