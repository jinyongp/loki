package process

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testManager(t *testing.T, maximum int) *Manager {
	t.Helper()
	m, err := NewManager(ManagerOptions{MaxProcesses: maximum, MaxOutputBytes: 4096,
		Retention: time.Minute, StopGrace: 50 * time.Millisecond, DrainGrace: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func startManaged(t *testing.T, m *Manager, argv ...string) (string, *managedProcess) {
	t.Helper()
	r, err := m.Start(StartSpec{Name: "fixture", Spec: Spec{Argv: argv, CWD: t.TempDir(), Timeout: 10 * time.Second, MaxOutput: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	id := r["session_id"].(string)
	p, err := m.get(id)
	if err != nil {
		t.Fatal(err)
	}
	return id, p
}

func awaitComplete(t *testing.T, p *managedProcess) {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not complete")
	}
}

func awaitOutput(t *testing.T, m *Manager, id, text string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, err := m.Read(id, nil, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(r["output"].(string), text) {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("process did not produce expected output")
	return nil
}

func TestManagedOutputCursorsAndUTF8(t *testing.T) {
	p := &managedProcess{id: "fixture", name: "name", maximum: 6, startedAt: time.Unix(0, 0), metadata: json.RawMessage(`{"port":32180}`)}
	p.Write([]byte("0123"))
	p.Write([]byte("456789"))
	zero := int64(0)
	r := p.snapshot(&zero, 2)
	if r["output"] != "45" || r["offset"] != int64(4) || r["next_offset"] != int64(6) || r["available_from"] != int64(4) || r["output_lost"] != true || r["has_more"] != true || r["exit_code"] != nil {
		t.Fatalf("tail cursor: %#v", r)
	}
	r["port"] = 7
	if p.snapshot(nil, 0)["port"] != json.Number("32180") {
		t.Fatal("snapshot mutated metadata")
	}
	for _, offset := range []int64{-4, 5, 9, 10, 99999} {
		r = p.snapshot(&offset, 4)
		if r["offset"].(int64) < p.base || r["next_offset"].(int64) < r["offset"].(int64) {
			t.Fatalf("invalid cursor: %#v", r)
		}
	}
	p.Write([]byte("\xff\xff\xe2\x82"))
	r = p.snapshot(nil, 6)
	if r["output"] != "89\ufffd\ufffd\ufffd" {
		t.Fatalf("Python replacement decoding: %q", r["output"])
	}
	p.Write([]byte("한글"))
	r = p.snapshot(nil, 4)
	if r["output"] != "한\ufffd" || r["next_offset"] != p.base+4 {
		t.Fatalf("UTF8 byte cursor: %#v", r)
	}
}

func TestManagedRealExitAndRepeatedStop(t *testing.T) {
	m := testManager(t, 2)
	id, p := startManaged(t, m, "/bin/sh", "-c", "printf out; printf err >&2; exit 7")
	awaitComplete(t, p)
	r, err := m.Read(id, nil, 65536)
	if err != nil || r["output"] != "outerr" || r["exit_code"] != 7 || r["timed_out"] != false || r["status"] != "exited" {
		t.Fatalf("exit: %#v %v", r, err)
	}
	for range 3 {
		r, err = m.Stop(id)
		if err != nil || r["exit_code"] != 7 {
			t.Fatalf("repeat stop: %#v %v", r, err)
		}
	}
	if _, err = m.Stop("missing"); err == nil || err.Error() != "unknown process session" {
		t.Fatalf("unknown session: %v", err)
	}
	if _, err = time.Parse(time.RFC3339Nano, r["started_at"].(string)); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDefaultStdinIsDevNull(t *testing.T) {
	m := testManager(t, 1)
	id, p := startManaged(t, m, "/usr/bin/awk", "BEGIN { print (getline value) }")
	awaitComplete(t, p)
	result, err := m.Read(id, nil, 16)
	if err != nil || result["output"] != "0\n" || result["exit_code"] != 0 {
		t.Fatalf("default stdin is not EOF: %#v %v", result, err)
	}
}

func TestManagedLimitsSingletonAndAdmissionFailures(t *testing.T) {
	m := testManager(t, 2)
	group, key, perGroup := "web", "repository-api", 1
	spec := StartSpec{Name: "api", Group: &group, InstanceKey: &key, MaxGroupProcesses: &perGroup,
		Spec: Spec{Argv: []string{"/usr/bin/sleep", "30"}, CWD: t.TempDir(), Timeout: time.Minute, MaxOutput: 4096}, Metadata: map[string]any{"port": 32180}}
	first, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Metadata["port"] = 32181
	second, err := m.Start(spec)
	if err != nil || second["session_id"] != first["session_id"] || second["reused"] != true || second["port"] != json.Number("32180") {
		t.Fatalf("reuse: %#v %v", second, err)
	}
	spec.InstanceKey = nil
	if _, err = m.Start(spec); err == nil || err.Error() != "maximum concurrent process count reached for profile web (1/1; total 1/2)" {
		t.Fatalf("profile limit: %v", err)
	}
	other := "api"
	spec.Group = &other
	if _, err = m.Start(spec); err != nil {
		t.Fatal(err)
	}
	spec.Group = &group
	if _, err = m.Start(spec); err == nil || err.Error() != "maximum concurrent process count reached (total 2/2, profile web 1/1)" {
		t.Fatalf("total limit: %v", err)
	}
	spec.InstanceKey = &key
	if _, err = m.Start(spec); err != nil {
		t.Fatalf("full capacity must still reuse: %v", err)
	}
	if m.Usage()["total"] != 2 || m.FindRunning(key)["session_id"] != first["session_id"] {
		t.Fatal("usage/reuse lookup")
	}
	m.Close()
	if m.Usage()["total"] != 0 {
		t.Fatal("close left running processes")
	}
	if _, err = m.Start(spec); err == nil {
		t.Fatal("closed manager accepted command")
	}
	otherManager := testManager(t, 1)
	for _, bad := range []StartSpec{
		{Spec: Spec{Argv: []string{"/not/an/executable"}, Timeout: time.Second, MaxOutput: 2}},
		{Spec: Spec{Argv: []string{"/usr/bin/true"}, Timeout: time.Second, MaxOutput: 2}, Metadata: map[string]any{"output": "spoofed"}},
		{},
	} {
		if _, err = otherManager.Start(bad); err == nil {
			t.Fatal("invalid admission succeeded")
		}
	}
	if otherManager.Usage()["total"] != 0 || len(otherManager.List()["processes"].([]map[string]any)) != 0 {
		t.Fatal("failed spawn consumed slot")
	}
}

func TestManagedConcurrentAdmissionReadsAndStop(t *testing.T) {
	m := testManager(t, 3)
	cwd := t.TempDir()
	var accepted atomic.Int32
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			_, err := m.Start(StartSpec{Name: "race", Spec: Spec{Argv: []string{"/usr/bin/sleep", "30"}, CWD: cwd, Timeout: time.Minute, MaxOutput: 4096}})
			if err == nil {
				accepted.Add(1)
			}
		})
	}
	workers.Wait()
	if accepted.Load() != 3 || m.Usage()["total"] != 3 {
		t.Fatalf("non-atomic admission: %d", accepted.Load())
	}
	for _, item := range m.List()["processes"].([]map[string]any) {
		id := item["session_id"].(string)
		for range 4 {
			workers.Go(func() {
				for range 10 {
					_, _ = m.Read(id, nil, 1)
					m.List()
					m.Usage()
				}
				_, _ = m.Stop(id)
			})
		}
	}
	workers.Wait()
	if m.Usage()["total"] != 0 {
		t.Fatal("concurrent stop left live process")
	}
}

func TestManagedTimeoutKillsTermIgnoringGroup(t *testing.T) {
	m := testManager(t, 1)
	r, err := m.Start(StartSpec{Name: "timeout", Spec: Spec{Argv: []string{"/bin/sh", "-c", "trap '' TERM; printf ready; sleep 30"}, CWD: t.TempDir(), Timeout: 250 * time.Millisecond, MaxOutput: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	id := r["session_id"].(string)
	awaitOutput(t, m, id, "ready")
	p, _ := m.get(id)
	awaitComplete(t, p)
	r, err = m.Read(id, nil, 4096)
	if err != nil || r["exit_code"] != -9 || r["timed_out"] != true || r["status"] != "exited" {
		t.Fatalf("timeout: %#v %v", r, err)
	}
}

func TestManagedParentExitCleansDescendants(t *testing.T) {
	for _, finish := range []string{"exit 7", "wait"} {
		t.Run(finish, func(t *testing.T) {
			m := testManager(t, 1)
			id, p := startManaged(t, m, "/bin/sh", "-c", "sleep 30 & printf 'child:%s\\n' \"$!\"; "+finish)
			r := awaitOutput(t, m, id, "child:")
			text := strings.TrimSpace(strings.TrimPrefix(r["output"].(string), "child:"))
			pid, err := strconv.Atoi(text)
			if err != nil {
				t.Fatalf("child pid: %q", text)
			}
			if finish == "wait" {
				if _, err = m.Stop(id); err != nil {
					t.Fatal(err)
				}
			}
			awaitComplete(t, p)
			// An orphan zombie may await the container's init reaper. It is not live.
			deadline := time.Now().Add(time.Second)
			for {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
				if os.IsNotExist(err) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				parts := strings.Fields(string(data)[strings.LastIndex(string(data), ")")+1:])
				if len(parts) > 0 && (parts[0] == "Z" || parts[0] == "X") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child survived process session termination")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

func TestManagedRetentionAndHistoryBound(t *testing.T) {
	m := testManager(t, 1)
	for range 36 {
		_, p := startManaged(t, m, "/usr/bin/true")
		awaitComplete(t, p)
	}
	items := m.List()["processes"].([]map[string]any)
	if len(items) != 32 {
		t.Fatalf("retained %d histories", len(items))
	}
	id := items[0]["session_id"].(string)
	p, _ := m.get(id)
	p.mu.Lock()
	p.completedAt = time.Now().Add(-2 * time.Minute)
	p.mu.Unlock()
	if _, err := m.Read(id, nil, 1); err == nil {
		t.Fatal("expired process retained")
	}
}
