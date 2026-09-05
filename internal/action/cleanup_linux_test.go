package action

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

type cleanupFixture struct {
	root                        string
	workspace, recovery, marker *os.File
}

func newCleanupFixture(t *testing.T) cleanupFixture {
	t.Helper()
	f := cleanupFixture{root: t.TempDir()}
	for name, destination := range map[string]**os.File{"workspace": &f.workspace, "recovery": &f.recovery} {
		p := filepath.Join(f.root, name)
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		*destination = file
		t.Cleanup(func() { file.Close() })
	}
	var err error
	f.marker, err = os.OpenFile(filepath.Join(f.root, "journal"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.marker.Close() })
	if err := claimMaterializationFile(f.workspace, f.marker, f.recovery, "target"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f cleanupFixture) target() string { return filepath.Join(f.root, "workspace", "target") }

func (f cleanupFixture) intent(t *testing.T) *materializationRecord {
	t.Helper()
	record, err := readMaterializationRecord(f.marker)
	if err != nil || record == nil {
		t.Fatalf("record: %v", err)
	}
	if err := cleanupIntent(f.workspace, f.recovery, f.marker, record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestMaterializationCleanupReplacementRace(t *testing.T) {
	for _, occupied := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "occupied"}[occupied], func(t *testing.T) {
			f := newCleanupFixture(t)
			record := f.intent(t)
			if err := os.Rename(f.target(), filepath.Join(f.root, "original")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.target(), []byte("replacement"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := captureMaterialization(f.workspace, f.recovery, f.marker, record); err != nil {
				t.Fatal(err)
			}
			if occupied {
				if err := os.WriteFile(f.target(), []byte("new occupant"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cleared, err := finishMaterialization(f.workspace, f.recovery, f.marker, record)
			if cleared || err == nil {
				t.Fatalf("replacement cleared: %v %v", cleared, err)
			}
			if occupied {
				if !errors.Is(err, retainedRecovery) {
					t.Fatal(err)
				}
				got, _ := os.ReadFile(f.target())
				if string(got) != "new occupant" {
					t.Fatal("overwrote occupied destination")
				}
				got, _ = os.ReadFile(filepath.Join(f.recovery.Name(), record.Quarantine))
				if string(got) != "replacement" {
					t.Fatal("lost captured replacement")
				}
				if err := os.Remove(f.target()); err != nil {
					t.Fatal(err)
				}
				if _, err := clearMaterialization(f.workspace, f.marker, f.recovery, "target"); !errors.Is(err, unsafeClear) {
					t.Fatal(err)
				}
			}
			got, _ := os.ReadFile(f.target())
			if string(got) != "replacement" {
				t.Fatal("replacement was not restored")
			}
		})
	}
}

func TestMaterializationCleanupRetainsOpenWriter(t *testing.T) {
	f := newCleanupFixture(t)
	writer, err := os.OpenFile(f.target(), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if cleared, err := clearMaterialization(f.workspace, f.marker, f.recovery, "target"); cleared || !errors.Is(err, unsafeClear) {
		t.Fatalf("open writer discarded: %v %v", cleared, err)
	}
	if _, err := writer.Write([]byte("user edit")); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(f.target())
	if string(got) != "user edit" {
		t.Fatal("writer's file lost")
	}
}

func TestMaterializationCleanupCrashRecovery(t *testing.T) {
	for _, stage := range []string{"intent", "rename", "captured", "torn-frame", "unlinked"} {
		t.Run(stage, func(t *testing.T) {
			f := newCleanupFixture(t)
			record := f.intent(t)
			if stage != "intent" {
				if err := unix.Renameat2(int(f.workspace.Fd()), "target", int(f.recovery.Fd()), record.Quarantine, unix.RENAME_NOREPLACE); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "captured" || stage == "torn-frame" || stage == "unlinked" {
				record.Phase = "captured"
				if err := writeMaterializationRecord(f.marker, record); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "torn-frame" {
				info, _ := f.marker.Stat()
				if _, err := f.marker.WriteAt([]byte(`{"record":`), info.Size()); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "unlinked" {
				if err := unix.Unlinkat(int(f.recovery.Fd()), record.Quarantine, 0); err != nil {
					t.Fatal(err)
				}
			}
			if cleared, err := clearMaterialization(f.workspace, f.marker, f.recovery, "target"); !cleared || err != nil {
				t.Fatalf("recovery: %v %v", cleared, err)
			}
			if _, err := os.Lstat(f.target()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("placeholder remains")
			}
			entries, _ := os.ReadDir(f.recovery.Name())
			if len(entries) != 0 {
				t.Fatal("quarantine remains")
			}
			info, _ := f.marker.Stat()
			if info.Size() != 0 {
				t.Fatal("journal not compacted")
			}
		})
	}
}

func TestMaterializationCleanupConcurrentReplacement(t *testing.T) {
	for range 100 {
		f := newCleanupFixture(t)
		userData := []byte("preserve user file")
		swap := filepath.Join(f.workspace.Name(), "swap")
		if err := os.WriteFile(swap, userData, 0600); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				_ = unix.Renameat2(int(f.workspace.Fd()), "target", int(f.workspace.Fd()), "swap", unix.RENAME_EXCHANGE)
			}
		}()
		_, _ = clearMaterialization(f.workspace, f.marker, f.recovery, "target")
		close(done)
		wg.Wait()
		found := 0
		for _, dir := range []string{f.workspace.Name(), f.recovery.Name()} {
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Equal(data, userData) {
					found++
				}
			}
		}
		if found != 1 {
			t.Fatalf("user file count after concurrent cleanup: %d", found)
		}
	}
}
