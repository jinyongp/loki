package process

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

func TestScopeResultSettlingAndReset(t *testing.T) {
	for _, reason := range []string{"oom-kill", "success", "signal"} {
		t.Run(reason, func(t *testing.T) {
			var calls []string
			unit := "loki-action-0123456789abcdef.scope"
			got := collectScopeResult(t.Context(), unit, func(_ context.Context, args ...string) (Result, error) {
				calls = append(calls, args[0])
				if len(calls) == 1 {
					return Result{Output: "ActiveState=active\nResult=success\n"}, nil
				}
				if args[0] == "reset-failed" {
					return Result{}, errors.New("reset unavailable")
				}
				return Result{Output: "ActiveState=failed\nResult=" + reason + "\n"}, nil
			}, time.Millisecond)
			if got != reason {
				t.Fatal(got)
			}
			want := []string{"show", "show"}
			if reason != "success" {
				want = append(want, "reset-failed")
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatal(calls)
			}
		})
	}
	called := false
	if result := collectScopeResult(t.Context(), "unrelated.service", func(context.Context, ...string) (Result, error) { called = true; return Result{}, nil }, 0); result != "" || called {
		t.Fatal("unrelated unit inspected")
	}
}
func TestScopeDiagnosticsBeforeCompletedSnapshot(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	m, err := NewManager(ManagerOptions{MaxProcesses: 1, MaxOutputBytes: 4096, Retention: time.Minute, ReadScopeResult: func(string) string { close(entered); <-release; return "oom-kill" }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	r, err := m.Start(StartSpec{Spec: Spec{Argv: []string{"/usr/bin/true"}, CWD: "/", Timeout: time.Second, MaxOutput: 4096}, ScopeUnit: "loki-action-0123456789abcdef.scope"})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	id := r["session_id"].(string)
	snapshot, err := m.Read(id, nil, 4096)
	close(release)
	if err != nil || snapshot["status"] != "running" {
		t.Fatalf("premature completion: %#v %v", snapshot, err)
	}
	snapshot, err = m.Stop(id)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot["status"] != "exited" || snapshot["systemd_result"] != "oom-kill" || snapshot["oom_killed"] != true || snapshot["termination_reason"] != "oom" {
		t.Fatal(snapshot)
	}
}

func TestSystemdOOMScope(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_SYSTEMD_TESTS") != "1" {
		t.Skip("explicit disposable systemd scope acceptance not requested")
	}
	if os.Geteuid() != 0 {
		t.Fatal("isolated systemd acceptance requires development root")
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	unit := "loki-action-" + hex.EncodeToString(suffix[:]) + ".scope"
	t.Cleanup(func() { exec.Command("/usr/bin/systemctl", "reset-failed", unit).Run() })
	m, err := NewManager(ManagerOptions{MaxProcesses: 1, MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	started, err := m.Start(StartSpec{ScopeUnit: unit, Spec: Spec{Argv: []string{"/usr/bin/systemd-run", "--scope", "--quiet", "--unit=" + unit, "--property=MemoryMax=32M", "--property=MemorySwapMax=0", "--", "/usr/bin/python3", "-c", "data=bytearray(128*1024*1024)"}, CWD: "/", Timeout: 20 * time.Second, MaxOutput: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	id := started["session_id"].(string)
	child, err := m.get(id)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.done:
	case <-time.After(25 * time.Second):
		t.Fatal("OOM fixture timeout")
	}
	result, err := m.Read(id, nil, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if result["exit_code"] == 0 || result["systemd_result"] != "oom-kill" || result["oom_killed"] != true || result["termination_reason"] != "oom" {
		t.Fatalf("OOM completion: %#v", result)
	}
}
