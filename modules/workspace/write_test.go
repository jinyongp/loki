package workspace

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
	"loki/internal/config"
	"loki/internal/fault"
)

func externalWriterOriginal() string { return "initial\n" + strings.Repeat("x", 32<<20) }

func TestExternalWriterChild(t *testing.T) {
	root := os.Getenv("LOKI_EXTERNAL_WRITER_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = root
	c.AuditLog = filepath.Join(os.Getenv("LOKI_EXTERNAL_WRITER_STATE"), "audit.jsonl")
	c.MaxWriteBytes = 40 << 20
	f, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = f.Replace("shared.txt", "initial", "changed", Digest([]byte(externalWriterOriginal())), 1)
	if fault.Describe(err).Code != fault.CodeConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestReplaceRejectsExternalWriteDuringPublication(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	target := filepath.Join(root, "shared.txt")
	if err := os.WriteFile(target, []byte(externalWriterOriginal()), 0600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, err = unix.InotifyAddWatch(fd, root, unix.IN_CREATE); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestExternalWriterChild$", "-test.timeout=20s")
	child.Env = append(os.Environ(), "LOKI_EXTERNAL_WRITER_ROOT="+root, "LOKI_EXTERNAL_WRITER_STATE="+state)
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		child.Process.Signal(syscall.SIGCONT)
		child.Process.Kill()
		child.Wait()
	})
	observed := false
	for !observed {
		events := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(events, 10000)
		if err != nil || n == 0 {
			t.Fatalf("notification wait: %d, %v", n, err)
		}
		buf := make([]byte, 8192)
		n, err = unix.Read(fd, buf)
		if err != nil {
			t.Fatal(err)
		}
		for pos := 0; pos+unix.SizeofInotifyEvent <= n; {
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[pos]))
			name := strings.TrimRight(string(buf[pos+unix.SizeofInotifyEvent:pos+unix.SizeofInotifyEvent+int(event.Len)]), "\x00")
			if strings.HasPrefix(name, ".loki-write-") {
				observed = true
			}
			pos += unix.SizeofInotifyEvent + int(event.Len)
		}
	}
	if err = child.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	var status unix.WaitStatus
	if _, err = unix.Wait4(child.Process.Pid, &status, unix.WUNTRACED, nil); err != nil || !status.Stopped() {
		t.Fatalf("pause: %v %v", status, err)
	}
	// At this point the production writer has created its publication temporary
	// file, after checking the expected hash and recording the prior revision.
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != externalWriterOriginal() {
		t.Fatal("missed race window; inconclusive")
	}
	if err = os.WriteFile(target, []byte("external-B"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = child.Process.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatalf("writer: %v\n%s", err, output.String())
	}
	after, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "external-B" {
		t.Fatal("external content was overwritten")
	}
	revisions, err := filepath.Glob(filepath.Join(state, "file-revisions", "*.bin"))
	if err != nil || len(revisions) != 1 {
		t.Fatalf("revisions: %v %v", revisions, err)
	}
	backup, err := os.ReadFile(revisions[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != externalWriterOriginal() {
		t.Fatal("unexpected revision content")
	}
	temporaries, err := filepath.Glob(filepath.Join(root, ".loki-write-*"))
	if err != nil || len(temporaries) != 0 {
		t.Fatalf("temporary not cleaned: %v %v", temporaries, err)
	}
	t.Log("external process changed A to B during preparation; edit rejected; B retained")
}

func TestWriteIfUnchangedReportsTargetChangesAsConflicts(t *testing.T) {
	for _, kind := range []string{"content", "missing", "directory", "parent-file", "symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			files := fixture(t)
			path := "target"
			target := filepath.Join(files.Policy.Root(), path)
			var err error
			switch kind {
			case "content":
				err = os.WriteFile(target, []byte("external content"), 0600)
			case "missing":
			case "directory":
				err = os.Mkdir(target, 0700)
			case "parent-file":
				err = os.WriteFile(target, []byte("external content"), 0600)
				path = "target/child"
			case "symlink":
				err = os.Symlink("outside", target)
			case "fifo":
				err = unix.Mkfifo(target, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = files.writeIfUnchanged(path, []byte("replacement"), 0600, Digest([]byte("original")))
			if detail := fault.Describe(err); detail.Code != fault.CodeConflict {
				t.Fatalf("changed %s: %#v (%v)", kind, detail, err)
			}
			switch kind {
			case "content", "parent-file":
				requireWorkspaceContent(t, files, "target", "external content")
			case "missing":
				if _, err := os.Lstat(target); !os.IsNotExist(err) {
					t.Fatalf("missing target created: %v", err)
				}
			default:
				info, err := os.Lstat(target)
				if err != nil || info.Mode().IsRegular() {
					t.Fatalf("external %s replaced: %v %v", kind, info, err)
				}
			}
			temporary, err := filepath.Glob(filepath.Join(files.Policy.Root(), ".loki-write-*"))
			if err != nil || len(temporary) != 0 {
				t.Fatalf("temporary not cleaned: %v %v", temporary, err)
			}
		})
	}
}
