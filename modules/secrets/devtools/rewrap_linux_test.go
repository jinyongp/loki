//go:build linux

package devtools

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestColdRewrapErasesCredentialFromInheritedDaemonDescriptor(t *testing.T) {
	t.Run("success", func(t *testing.T) { testColdRewrapDescriptor(t, false) })
	t.Run("authentication_failure", func(t *testing.T) { testColdRewrapDescriptor(t, true) })
}

func testColdRewrapDescriptor(t *testing.T, rejected bool) {
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("pinned provider required")
		}
		t.Skip("native values suite supplies exact provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := filepath.Join(t.TempDir(), "provider")
	p, err := New(ctx, binary, root)
	if err != nil {
		t.Fatal(err)
	}
	old, next := []byte("synthetic-cold-original"), []byte("synthetic-cold-target")
	defer clear(old)
	defer clear(next)
	// Only owned synthetic fixture processes are inspected or stopped. No
	// production adapter discovers daemon PIDs or provider-private paths.
	daemons := func() []int {
		var pids []int
		entries, e := os.ReadDir("/proc")
		if e != nil {
			t.Fatal(e)
		}
		for _, entry := range entries {
			pid, e := strconv.Atoi(entry.Name())
			if e != nil {
				continue
			}
			cmd, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
			if e == nil && bytes.Contains(cmd, []byte(root)) && bytes.Contains(cmd, []byte("__vault-session")) {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	t.Cleanup(func() {
		for _, pid := range daemons() {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
	})
	if _, err := p.Protection(ctx, "init", old); err != nil {
		t.Fatal(err)
	}
	initial := daemons()
	if len(initial) != 1 {
		t.Fatal("owned daemon not found")
	}
	if syscall.Kill(initial[0], syscall.SIGTERM) != nil {
		t.Fatal("cannot stop owned daemon")
	}
	for len(daemons()) != 0 {
		select {
		case <-ctx.Done():
			t.Fatal("owned daemon did not stop")
		case <-time.After(10 * time.Millisecond):
		}
	}
	credential := old
	if rejected {
		credential = []byte("wrong-original")
	}
	if _, err := p.Rewrap(ctx, "91883ec9-e6c1-444f-bd52-70c8de38a661", credential, next); (err != nil) != rejected {
		t.Fatal("unexpected cold rewrap authentication result", err)
	}
	current := daemons()
	if len(current) != 1 || current[0] == initial[0] {
		t.Fatal("rewrap did not start a fresh daemon")
	}
	for _, pid := range current {
		fdRoot := filepath.Join("/proc", strconv.Itoa(pid), "fd")
		fds, err := os.ReadDir(fdRoot)
		if err != nil {
			t.Fatal(err)
		}
		for _, fd := range fds {
			path := filepath.Join(fdRoot, fd.Name())
			target, err := os.Readlink(path)
			if err != nil || !strings.Contains(target, "memfd:loki-protection") {
				continue
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal("cannot inspect owned inherited descriptor")
			}
			zero := bytes.Equal(body, make([]byte, len(body)))
			clear(body)
			if !zero {
				t.Fatal("credential survived the provider response in an inherited descriptor")
			}
		}
	}
	target := next
	if rejected {
		target = old
	}
	if _, err := p.Protection(ctx, "unlock", target); err != nil {
		t.Fatal("clearing transport changed installed target protection", err)
	}
}
